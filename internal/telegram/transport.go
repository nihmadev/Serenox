// Package telegram owns session persistence and update/action transport. Its
// public interfaces use only protocol DTOs, never a concrete Telegram library.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/dispatch"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrClosed         = errors.New("telegram transport closed")
	ErrIngressFull    = errors.New("telegram ingress queue full")
	ErrActionLimit    = errors.New("telegram action batch limit exceeded")
	ErrReconnectLimit = errors.New("telegram reconnect limit reached")
)

type Sink interface {
	HandleUpdate(context.Context, pluginv1.Update) error
}

type SinkFunc func(context.Context, pluginv1.Update) error

func (fn SinkFunc) HandleUpdate(ctx context.Context, update pluginv1.Update) error {
	return fn(ctx, update)
}

// Transport is implemented by the live client adapter and recorded fixtures.
// Run blocks until the context ends or the transport fails.
type Transport interface {
	Run(context.Context, Sink) error
	Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error)
	Close(context.Context) error
}

// Client is the narrow adapter a Telegram library must implement. NextUpdate
// must unblock when ctx is canceled or Close is called.
type Client interface {
	Connect(context.Context) error
	NextUpdate(context.Context) (pluginv1.Update, error)
	Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error)
	Close(context.Context) error
}

type ReconnectPolicy struct {
	Attempts int
	Initial  time.Duration
	Maximum  time.Duration
}

func (p ReconnectPolicy) normalized() (ReconnectPolicy, error) {
	if p.Attempts == 0 {
		p.Attempts = 5
	}
	if p.Initial == 0 {
		p.Initial = 100 * time.Millisecond
	}
	if p.Maximum == 0 {
		p.Maximum = 5 * time.Second
	}
	if p.Attempts < 1 || p.Attempts > 100 || p.Initial <= 0 || p.Maximum < p.Initial || p.Maximum > time.Minute {
		return ReconnectPolicy{}, errors.New("invalid Telegram reconnect policy")
	}
	return p, nil
}

// ClientTransport adds bounded reconnect behavior around a native adapter. It
// creates at most one reconnect timer and never one timer per update.
type ClientTransport struct {
	client Client
	policy ReconnectPolicy
	mu     sync.Mutex
	closed bool
}

func NewClientTransport(client Client, policy ReconnectPolicy) (*ClientTransport, error) {
	if client == nil {
		return nil, errors.New("nil Telegram client")
	}
	normalized, err := policy.normalized()
	if err != nil {
		return nil, err
	}
	return &ClientTransport{client: client, policy: normalized}, nil
}

func (t *ClientTransport) Run(ctx context.Context, sink Sink) error {
	if sink == nil {
		return errors.New("nil Telegram update sink")
	}
	if t.isClosed() {
		return ErrClosed
	}
	delay := t.policy.Initial
	var last error
	for attempt := 0; attempt < t.policy.Attempts; attempt++ {
		if err := t.client.Connect(ctx); err != nil {
			last = err
		} else {
			last = t.consume(ctx, sink)
			if last == nil || errors.Is(last, context.Canceled) || errors.Is(last, ErrClosed) {
				return last
			}
		}
		if attempt+1 == t.policy.Attempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		if delay < t.policy.Maximum {
			delay *= 2
			if delay > t.policy.Maximum {
				delay = t.policy.Maximum
			}
		}
	}
	return fmt.Errorf("%w after %d attempts: %v", ErrReconnectLimit, t.policy.Attempts, last)
}

func (t *ClientTransport) consume(ctx context.Context, sink Sink) error {
	for {
		update, err := t.client.NextUpdate(ctx)
		if err != nil {
			return err
		}
		if err := update.Validate(); err != nil {
			return fmt.Errorf("invalid Telegram update: %w", err)
		}
		if err := sink.HandleUpdate(ctx, update); err != nil {
			return fmt.Errorf("handle Telegram update: %w", err)
		}
	}
}

func (t *ClientTransport) Apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if t.isClosed() {
		return pluginv1.ActionResult{}, ErrClosed
	}
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, fmt.Errorf("invalid Telegram action: %w", err)
	}
	return t.client.Apply(ctx, action)
}

func (t *ClientTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	return t.client.Close(ctx)
}

func (t *ClientTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// ApplyBatch bounds both collection size and in-flight Telegram requests by
// applying actions sequentially on the caller's owned worker.
func ApplyBatch(ctx context.Context, transport Transport, actions []pluginv1.Action, maximum int) ([]pluginv1.ActionResult, error) {
	if transport == nil || maximum <= 0 {
		return nil, errors.New("invalid action batch configuration")
	}
	if len(actions) > maximum {
		return nil, ErrActionLimit
	}
	results := make([]pluginv1.ActionResult, 0, len(actions))
	for _, action := range actions {
		result, err := transport.Apply(ctx, action)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// ToDispatchMessage converts the stable DTO into the subset used by command
// and watcher filtering.
func ToDispatchMessage(update pluginv1.Update) (dispatch.Message, bool) {
	if update.Message == nil {
		return dispatch.Message{}, false
	}
	message := update.Message
	media := dispatch.MediaNone
	if message.Media != nil {
		switch message.Media.Kind {
		case "photo":
			media = dispatch.MediaPhoto
		case "video":
			media = dispatch.MediaVideo
		case "audio", "voice":
			media = dispatch.MediaAudio
		case "document":
			media = dispatch.MediaDocument
		case "sticker":
			media = dispatch.MediaSticker
		case "dice":
			media = dispatch.MediaDice
		default:
			media = dispatch.MediaOther
		}
	}
	chatKind := dispatch.ChatUnknown
	switch message.ChatKind {
	case "private":
		chatKind = dispatch.ChatPrivate
	case "group":
		chatKind = dispatch.ChatGroup
	case "supergroup":
		chatKind = dispatch.ChatSupergroup
	case "channel":
		chatKind = dispatch.ChatChannel
	}
	return dispatch.Message{
		ID: message.ID, ChatID: message.ChatID, SenderID: message.SenderID,
		UpdateKind: string(update.Kind),
		Text:       message.Text, Outgoing: message.Outgoing, ChatKind: chatKind, MediaKind: media,
		ViaBot: message.ViaBot, Forwarded: message.Forwarded,
		Reply: message.ReplyToMessageID != 0, Mentioned: message.Mentioned,
		Editable: message.Outgoing, Edited: update.Kind == pluginv1.UpdateEditedMessage,
	}, true
}

func normalizeRunError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
