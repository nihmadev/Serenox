package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrNotAuthorized  = errors.New("Telegram session is not authorized; run authorization before starting live transport")
	ErrAlreadyStarted = errors.New("Telegram live transport has already been started")
)

type DeviceInfo struct {
	DeviceModel    string
	SystemVersion  string
	AppVersion     string
	SystemLangCode string
	LangPack       string
	LangCode       string
}

func (d DeviceInfo) gotd() gotdtelegram.DeviceConfig {
	return gotdtelegram.DeviceConfig{
		DeviceModel: d.DeviceModel, SystemVersion: d.SystemVersion, AppVersion: d.AppVersion,
		SystemLangCode: d.SystemLangCode, LangPack: d.LangPack, LangCode: d.LangCode,
	}
}

type LiveConfig struct {
	AppID             int
	AppHash           string
	Session           SessionStore
	UpdateCapacity    int
	PeerCacheCapacity int
	Device            DeviceInfo
}

func (c LiveConfig) normalized() (LiveConfig, error) {
	if c.AppID <= 0 || int64(c.AppID) > math.MaxInt32 || c.AppHash == "" || c.Session == nil {
		return LiveConfig{}, errors.New("live Telegram requires app ID, app hash, and session storage")
	}
	if c.UpdateCapacity == 0 {
		c.UpdateCapacity = 256
	}
	if c.PeerCacheCapacity == 0 {
		c.PeerCacheCapacity = 4096
	}
	if c.UpdateCapacity < 1 || c.UpdateCapacity > 65_536 {
		return LiveConfig{}, errors.New("Telegram update capacity must be between 1 and 65536")
	}
	if c.PeerCacheCapacity < 1 || c.PeerCacheCapacity > 65_536 {
		return LiveConfig{}, errors.New("Telegram peer cache capacity must be between 1 and 65536")
	}
	return c, nil
}

// LiveTransport is the native gotd/td user-session transport. Its only
// application-facing values are stable plugin protocol DTOs.
type LiveTransport struct {
	client  *gotdtelegram.Client
	api     actionAPI
	updates chan pluginv1.Update
	peers   *peerBook

	mu              sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	started         bool
	running         bool
	closed          bool
	identity        Identity
	mediaDownloader MediaDownloader
	readService     *ReadService
	moderation      *ModerationService
}

// MediaDownloader is the daemon-owned attachment store used for outbound
// plugin media actions. It keeps Telegram transport code independent of the
// store implementation.
type MediaDownloader interface {
	DownloadAny(context.Context, string, io.Writer) error
}

func (t *LiveTransport) SetMediaDownloader(value MediaDownloader) {
	t.mu.Lock()
	t.mediaDownloader = value
	t.mu.Unlock()
}

func NewLive(config LiveConfig) (*LiveTransport, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	live := &LiveTransport{
		updates: make(chan pluginv1.Update, config.UpdateCapacity),
		peers:   newPeerBook(config.PeerCacheCapacity),
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("create read cursor secret: %w", err)
	}
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
		return live.enqueueMessage(ctx, pluginv1.UpdateMessage, entities, update.Message)
	})
	dispatcher.OnEditMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
		return live.enqueueMessage(ctx, pluginv1.UpdateEditedMessage, entities, update.Message)
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
		return live.enqueueMessage(ctx, pluginv1.UpdateMessage, entities, update.Message)
	})
	dispatcher.OnEditChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
		return live.enqueueMessage(ctx, pluginv1.UpdateEditedMessage, entities, update.Message)
	})
	dispatcher.OnDeleteMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteMessages) error {
		return live.enqueueDeleted(ctx, "messages", 0, update.Pts, update.Messages)
	})
	dispatcher.OnDeleteChannelMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
		return live.enqueueDeleted(ctx, "channel", update.ChannelID, update.Pts, update.Messages)
	})
	live.client = gotdtelegram.NewClient(config.AppID, config.AppHash, gotdtelegram.Options{
		SessionStorage: gotdSessionStorage{store: config.Session},
		UpdateHandler:  dispatcher,
		Device:         config.Device.gotd(),
	})
	live.api = live.client.API()
	cursor, err := NewCursorCodec(secret, time.Duration(pluginv1.MaxClientCursorLifetimeSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	live.readService, err = NewReadService(live, cursor, 8)
	if err != nil {
		return nil, err
	}
	live.moderation, err = NewModerationService(live, 8, 4096)
	if err != nil {
		return nil, err
	}
	return live, nil
}

func (t *LiveTransport) Run(ctx context.Context, sink Sink) error {
	if ctx == nil || sink == nil {
		return errors.New("live Telegram requires context and update sink")
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	if t.started {
		t.mu.Unlock()
		return ErrAlreadyStarted
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.started, t.running, t.cancel, t.done = true, true, cancel, done
	t.mu.Unlock()

	defer func() {
		cancel()
		t.mu.Lock()
		t.running = false
		close(done)
		t.mu.Unlock()
	}()
	err := t.client.Run(runCtx, func(clientCtx context.Context) error {
		identity, verifyErr := verifyIdentity(clientCtx, t.client)
		if verifyErr != nil {
			return verifyErr
		}
		t.mu.Lock()
		t.identity = identity
		t.mu.Unlock()
		t.peers.setSelf(identity.UserID)
		return t.consume(clientCtx, sink)
	})
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (t *LiveTransport) consume(ctx context.Context, sink Sink) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-t.updates:
			if err := sink.HandleUpdate(ctx, update); err != nil {
				return fmt.Errorf("handle Telegram update: %w", err)
			}
		}
	}
}

func (t *LiveTransport) enqueueMessage(ctx context.Context, kind pluginv1.UpdateKind, entities tg.Entities, message tg.MessageClass) error {
	t.peers.learn(entities)
	t.mu.Lock()
	selfID := t.identity.UserID
	t.mu.Unlock()
	update, ok, err := convertMessage(kind, message, entities, selfID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := update.Validate(); err != nil {
		return fmt.Errorf("convert Telegram update: %w", err)
	}
	return t.enqueue(ctx, update)
}

func (t *LiveTransport) enqueueDeleted(ctx context.Context, scope string, channelID int64, pts int, messageIDs []int) error {
	if len(messageIDs) == 0 {
		return nil
	}
	if len(messageIDs) > pluginv1.MaxCollectionItems {
		return fmt.Errorf("Telegram deleted-message update exceeds %d IDs", pluginv1.MaxCollectionItems)
	}
	deleted := make([]int64, len(messageIDs))
	for index, messageID := range messageIDs {
		if messageID <= 0 {
			return fmt.Errorf("Telegram deleted-message update contains invalid ID %d", messageID)
		}
		deleted[index] = int64(messageID)
	}
	update := pluginv1.Update{
		ID:         fmt.Sprintf("telegram:deleted:%s:%d:%d", scope, channelID, pts),
		Kind:       pluginv1.UpdateDeleted,
		DeletedIDs: deleted,
	}
	if err := update.Validate(); err != nil {
		return fmt.Errorf("convert Telegram deleted-message update: %w", err)
	}
	return t.enqueue(ctx, update)
}

func (t *LiveTransport) enqueue(ctx context.Context, update pluginv1.Update) error {
	// Blocking the gotd update handler is intentional bounded backpressure.
	// Cancellation always releases the producer; no goroutine is created per update.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case t.updates <- update:
		return nil
	}
}

func (t *LiveTransport) Identity() (Identity, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.identity, t.identity.UserID != 0
}

func (t *LiveTransport) Read(ctx context.Context, scope ReadScope, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	if t.readService == nil {
		return pluginv1.ClientResult{}, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Operation: string(request.Operation), Message: "Telegram read service is unavailable"}
	}
	return t.readService.Read(ctx, scope, request)
}

func (t *LiveTransport) Moderate(ctx context.Context, scope ModerationScope, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
	if t.moderation == nil {
		return moderationFailure(request, pluginv1.ErrorUnsupported, "Telegram moderation service is unavailable", false)
	}
	return t.moderation.Moderate(ctx, scope, request)
}

func (t *LiveTransport) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil close context")
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel, done, running := t.cancel, t.done, t.running
	t.mu.Unlock()
	if t.moderation != nil {
		t.moderation.Close()
	}
	if cancel != nil {
		cancel()
	}
	if !running || done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

var _ Transport = (*LiveTransport)(nil)
var _ ReadClient = (*LiveTransport)(nil)
var _ ModerationClient = (*LiveTransport)(nil)
