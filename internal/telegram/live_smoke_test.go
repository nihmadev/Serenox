package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// Live smoke tests are deliberately test-only. They never run in the normal
// package test because the gate must be set explicitly and every input is
// supplied for one invocation only.
const (
	liveSmokeGate           = "SERENOX_LIVE_TELEGRAM"
	liveSmokeAPIID          = "SERENOX_LIVE_TELEGRAM_API_ID"
	liveSmokeAPIHash        = "SERENOX_LIVE_TELEGRAM_API_HASH"
	liveSmokeCredentials    = "SERENOX_LIVE_TELEGRAM_CREDENTIALS"
	liveSmokeSession        = "SERENOX_LIVE_TELEGRAM_SESSION_PATH"
	liveSmokePeer           = "SERENOX_LIVE_TELEGRAM_PEER"
	liveSmokeTimeout        = "SERENOX_LIVE_TELEGRAM_TIMEOUT"
	liveSmokeMedia          = "SERENOX_LIVE_TELEGRAM_MEDIA"
	liveSmokeCleanupMarker  = "SERENOX_LIVE_TELEGRAM_CLEANUP_MARKER"
	liveSmokeDefaultTimeout = 90 * time.Second
)

type liveSmokeConfig struct {
	AppID         int
	AppHash       string
	SessionPath   string
	Peer          string
	Timeout       time.Duration
	CleanupMarker string
}

type liveSmokeCredentialFile struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

func TestLiveSmokeInputValidationIsCredentialFreeAndBounded(t *testing.T) {
	directory := t.TempDir()
	sessionPath := filepath.Join(directory, "telegram.session.json")
	if err := os.WriteFile(sessionPath, []byte("opaque-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		liveSmokeAPIID:   "12345",
		liveSmokeAPIHash: "0123456789abcdef0123456789abcdef",
		liveSmokeSession: sessionPath,
		liveSmokePeer:    "self",
	}
	getenv := func(key string) string { return values[key] }
	config, err := loadLiveSmokeConfig(getenv)
	if err != nil || config.AppID != 12345 || config.Peer != "self" {
		t.Fatalf("validated config=%#v err=%v", config, err)
	}

	values[liveSmokeAPIHash] = "not-a-hash"
	if _, err := loadLiveSmokeConfig(getenv); err == nil {
		t.Fatal("malformed API hash was accepted")
	}
	values[liveSmokeAPIHash] = "0123456789abcdef0123456789abcdef"
	values[liveSmokePeer] = "https://example.invalid"
	if _, err := loadLiveSmokeConfig(getenv); err == nil {
		t.Fatal("unbounded peer syntax was accepted")
	}
}

func TestLiveSmokeCredentialFileIsExplicitAndSizeBounded(t *testing.T) {
	directory := t.TempDir()
	credentialsPath := filepath.Join(directory, "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"api_id":12345,"api_hash":"0123456789abcdef0123456789abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(directory, "telegram.session.json")
	if err := os.WriteFile(sessionPath, []byte("opaque-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		liveSmokeCredentials: credentialsPath,
		liveSmokeSession:     sessionPath,
		liveSmokePeer:        "self",
	}
	config, err := loadLiveSmokeConfig(func(key string) string { return values[key] })
	if err != nil || config.AppID != 12345 || config.AppHash == "" {
		t.Fatalf("credential-file config=%#v err=%v", config, err)
	}
	if err := os.WriteFile(credentialsPath, make([]byte, 4097), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLiveSmokeCredentials(credentialsPath); err == nil {
		t.Fatal("oversized credential file was accepted")
	}
}

func TestLiveTelegramSmoke(t *testing.T) {
	gate := strings.TrimSpace(os.Getenv(liveSmokeGate))
	if gate != "1" && gate != "dry-run" {
		t.Skip("set SERENOX_LIVE_TELEGRAM=1 to run the opt-in Telegram smoke test")
	}
	config, err := loadLiveSmokeConfig(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if gate == "dry-run" {
		t.Logf("validated live Telegram inputs for peer kind %s; no Telegram connection or mutation was performed", smokePeerKind(config.Peer))
		return
	}

	transportConfig, err := NewFileSession(config.SessionPath, MaximumSessionBytes)
	if err != nil {
		t.Fatal(err)
	}
	live, err := NewLive(LiveConfig{
		AppID:             config.AppID,
		AppHash:           config.AppHash,
		Session:           transportConfig,
		UpdateCapacity:    64,
		PeerCacheCapacity: 64,
		Device: DeviceInfo{
			DeviceModel:    "Serenox-live-smoke",
			AppVersion:     "0.1.0-live-smoke",
			LangCode:       "en",
			SystemLangCode: "en-US",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	updates := make(chan pluginv1.Update, 64)
	runState := &smokeRunState{done: make(chan struct{})}
	go func() {
		err := live.Run(ctx, SinkFunc(func(_ context.Context, update pluginv1.Update) error {
			select {
			case updates <- update:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}))
		runState.mu.Lock()
		runState.err = err
		close(runState.done)
		runState.mu.Unlock()
	}()

	created := make(map[int64]struct{}, 2)
	var targetPeerID int64
	marker := smokeMarker()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, messageID := range findSmokeMessageIDs(cleanupCtx, live, targetPeerID, marker, config.CleanupMarker) {
			created[messageID] = struct{}{}
		}
		for messageID := range created {
			if _, cleanupErr := live.Apply(cleanupCtx, pluginv1.Action{
				ID: fmt.Sprintf("smoke-cleanup-%d", messageID), Kind: pluginv1.ActionDelete,
				ChatID: targetPeerID, MessageID: messageID,
			}); cleanupErr != nil {
				t.Logf("best-effort cleanup failed for one smoke message: %s", redactSmokeError(cleanupErr, config, ""))
			}
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if closeErr := live.Close(closeCtx); closeErr != nil {
			t.Logf("live transport close: %s", redactSmokeError(closeErr, config, ""))
		}
		closeCancel()
		select {
		case <-runState.done:
		case <-time.After(15 * time.Second):
			t.Log("live smoke transport did not report termination before the cleanup deadline")
		}
	}()

	identity, err := waitSmokeIdentity(ctx, live, runState)
	if err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("identity verification: %w", err), config, ""))
	}
	if identity.UserID == 0 {
		t.Fatal("identity verification returned an empty Telegram user ID")
	}
	peerID, err := smokeResolvePeer(ctx, live, identity, config.Peer)
	if err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("target peer verification: %w", err), config, ""))
	}
	targetPeerID = peerID

	if _, err := live.Apply(ctx, pluginv1.Action{ID: "smoke-send", Kind: pluginv1.ActionSend, ChatID: peerID, Text: marker}); err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("send operation: %w", err), config, marker))
	}
	sent, err := waitSmokeMessage(ctx, updates, runState, func(message *pluginv1.Message, update pluginv1.Update) bool {
		return update.Kind == pluginv1.UpdateMessage && message.ChatID == peerID && message.Text == marker
	})
	if err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("observe sent update: %w", err), config, marker))
	}
	created[sent.ID] = struct{}{}
	if sent.ID <= 0 || sent.ChatID != peerID || !sent.Outgoing {
		t.Fatalf("sent update failed DTO checks: id_valid=%t chat_match=%t outgoing=%t", sent.ID > 0, sent.ChatID == peerID, sent.Outgoing)
	}

	editedMarker := marker + " edited"
	if _, err := live.Apply(ctx, pluginv1.Action{ID: "smoke-edit", Kind: pluginv1.ActionEdit, ChatID: peerID, MessageID: sent.ID, Text: "<b>" + editedMarker + "</b>", ParseMode: "HTML"}); err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("edit operation: %w", err), config, marker))
	}
	edited, err := waitSmokeMessage(ctx, updates, runState, func(message *pluginv1.Message, update pluginv1.Update) bool {
		return update.Kind == pluginv1.UpdateEditedMessage && message.ChatID == peerID && message.ID == sent.ID && message.Text == editedMarker
	})
	if err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("observe edited update: %w", err), config, marker))
	}
	if len(edited.Entities) != 1 || edited.Entities[0].Type != "bold" {
		t.Fatalf("edited HTML entity conversion failed: entity_count=%d", len(edited.Entities))
	}

	replyMarker := marker + " reply"
	if _, err := live.Apply(ctx, pluginv1.Action{ID: "smoke-reply", Kind: pluginv1.ActionReply, ChatID: peerID, MessageID: sent.ID, Text: replyMarker}); err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("reply operation: %w", err), config, marker))
	}
	reply, err := waitSmokeMessage(ctx, updates, runState, func(message *pluginv1.Message, update pluginv1.Update) bool {
		return update.Kind == pluginv1.UpdateMessage && message.ChatID == peerID && message.Text == replyMarker
	})
	if err != nil {
		t.Fatal(redactSmokeError(fmt.Errorf("observe reply update: %w", err), config, marker))
	}
	created[reply.ID] = struct{}{}
	if reply.ReplyToMessageID != sent.ID {
		t.Fatalf("reply linkage mismatch: linked_to_created_message=%t", reply.ReplyToMessageID == sent.ID)
	}

	for messageID := range created {
		if _, err := live.Apply(ctx, pluginv1.Action{ID: fmt.Sprintf("smoke-delete-%d", messageID), Kind: pluginv1.ActionDelete, ChatID: peerID, MessageID: messageID}); err != nil {
			t.Fatal(redactSmokeError(fmt.Errorf("delete operation: %w", err), config, marker))
		}
		if err := waitSmokeDeletion(ctx, updates, runState, messageID); err != nil {
			t.Fatal(redactSmokeError(fmt.Errorf("observe deletion update: %w", err), config, marker))
		}
		delete(created, messageID)
	}
	t.Log("live Telegram smoke succeeded for identity, saved-messages peer, send, update ingestion, HTML edit, reply linkage, and deletion; media was not requested")
}

type smokeRunState struct {
	mu   sync.Mutex
	err  error
	done chan struct{}
}

func (s *smokeRunState) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func loadLiveSmokeConfig(getenv func(string) string) (liveSmokeConfig, error) {
	if getenv == nil {
		return liveSmokeConfig{}, errors.New("live Telegram smoke environment reader is nil")
	}
	apiIDValue := strings.TrimSpace(getenv(liveSmokeAPIID))
	apiHash := strings.TrimSpace(getenv(liveSmokeAPIHash))
	if credentialsPath := strings.TrimSpace(getenv(liveSmokeCredentials)); credentialsPath != "" && (apiIDValue == "" || apiHash == "") {
		credentials, err := readLiveSmokeCredentials(credentialsPath)
		if err != nil {
			return liveSmokeConfig{}, err
		}
		if apiIDValue == "" {
			apiIDValue = strconv.Itoa(credentials.APIID)
		}
		if apiHash == "" {
			apiHash = credentials.APIHash
		}
	}
	apiID, err := strconv.ParseInt(apiIDValue, 10, 32)
	if err != nil || apiID <= 0 {
		return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_API_ID must be a positive 32-bit integer")
	}
	if err := validateSmokeAPIHash(apiHash); err != nil {
		return liveSmokeConfig{}, err
	}
	sessionPath := strings.TrimSpace(getenv(liveSmokeSession))
	if sessionPath == "" || strings.IndexByte(sessionPath, 0) >= 0 || !filepath.IsAbs(sessionPath) {
		return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_SESSION_PATH must be an absolute path")
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_SESSION_PATH must point to an existing session file")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaximumSessionBytes {
		return liveSmokeConfig{}, errors.New("Telegram smoke session file is empty, non-regular, or exceeds the session limit")
	}
	peer := strings.TrimSpace(getenv(liveSmokePeer))
	if err := validateSmokePeer(peer); err != nil {
		return liveSmokeConfig{}, err
	}
	timeout := liveSmokeDefaultTimeout
	if raw := strings.TrimSpace(getenv(liveSmokeTimeout)); raw != "" {
		timeout, err = time.ParseDuration(raw)
		if err != nil {
			return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_TIMEOUT must be a duration")
		}
	}
	if timeout < 15*time.Second || timeout > 5*time.Minute {
		return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_TIMEOUT must be between 15s and 5m")
	}
	if strings.TrimSpace(getenv(liveSmokeMedia)) != "" {
		return liveSmokeConfig{}, errors.New("live media smoke is not enabled by this test; unset SERENOX_LIVE_TELEGRAM_MEDIA")
	}
	cleanupMarker := strings.TrimSpace(getenv(liveSmokeCleanupMarker))
	if len(cleanupMarker) > 128 || strings.ContainsAny(cleanupMarker, "\x00\r\n") {
		return liveSmokeConfig{}, errors.New("SERENOX_LIVE_TELEGRAM_CLEANUP_MARKER is invalid or too long")
	}
	return liveSmokeConfig{AppID: int(apiID), AppHash: apiHash, SessionPath: sessionPath, Peer: peer, Timeout: timeout, CleanupMarker: cleanupMarker}, nil
}

func readLiveSmokeCredentials(path string) (liveSmokeCredentialFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return liveSmokeCredentialFile{}, errors.New("SERENOX_LIVE_TELEGRAM_CREDENTIALS could not be opened")
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(payload) > 4096 {
		return liveSmokeCredentialFile{}, errors.New("live Telegram credentials file is unreadable or too large")
	}
	var credentials liveSmokeCredentialFile
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil || credentials.APIID <= 0 || validateSmokeAPIHash(credentials.APIHash) != nil {
		return liveSmokeCredentialFile{}, errors.New("live Telegram credentials file is malformed")
	}
	return credentials, nil
}

func findSmokeMessageIDs(ctx context.Context, live *LiveTransport, peerID int64, markers ...string) []int64 {
	if live == nil || peerID == 0 || len(markers) == 0 {
		return nil
	}
	history, ok := live.api.(interface {
		MessagesGetHistory(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return nil
	}
	peer, err := live.peers.resolve(peerID)
	if err != nil {
		return nil
	}
	result, err := history.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 100})
	if err != nil {
		return nil
	}
	messages, entities := messagesAndEntities(result)
	allowed := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		if marker != "" {
			allowed[marker] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(allowed))
	for _, raw := range messages {
		message, ok := raw.(*tg.Message)
		if !ok || message == nil || !message.Out || message.PeerID == nil {
			continue
		}
		chatID, _, found := peerChat(message.PeerID, entities)
		if !found || chatID != peerID {
			continue
		}
		if _, found := allowed[message.Message]; found {
			ids = append(ids, int64(message.ID))
		}
	}
	return ids
}

func validateSmokeAPIHash(value string) error {
	if len(value) != 32 {
		return errors.New("SERENOX_LIVE_TELEGRAM_API_HASH must be 32 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("SERENOX_LIVE_TELEGRAM_API_HASH must be 32 hexadecimal characters")
	}
	return nil
}

func validateSmokePeer(value string) error {
	if value == "self" || strings.HasPrefix(value, "@") {
		if value == "@" || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return errors.New("SERENOX_LIVE_TELEGRAM_PEER must be self, an integer ID, or @username")
		}
		return nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id == 0 || id == math.MinInt64 {
		return errors.New("SERENOX_LIVE_TELEGRAM_PEER must be self, an integer ID, or @username")
	}
	return nil
}

func smokePeerKind(value string) string {
	if value == "self" {
		return "self"
	}
	if strings.HasPrefix(value, "@") {
		return "username"
	}
	return "numeric"
}

func smokeResolvePeer(ctx context.Context, live *LiveTransport, identity Identity, value string) (int64, error) {
	if value == "self" {
		return identity.UserID, nil
	}
	if strings.HasPrefix(value, "@") {
		peer, err := live.ResolvePeer(ctx, 0, value)
		if err != nil || peer == nil || peer.ID == 0 {
			return 0, errors.New("configured username peer is inaccessible")
		}
		return peer.ID, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errors.New("configured numeric peer is invalid")
	}
	if _, err := live.ResolvePeer(ctx, id, ""); err != nil {
		return 0, errors.New("configured numeric peer is not resolved in the live session")
	}
	return id, nil
}

func waitSmokeIdentity(ctx context.Context, live *LiveTransport, runState *smokeRunState) (Identity, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if identity, ok := live.Identity(); ok {
			return identity, nil
		}
		select {
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		case <-runState.done:
			err := runState.error()
			if err == nil {
				return Identity{}, errors.New("live transport stopped before identity verification")
			}
			return Identity{}, err
		case <-ticker.C:
		}
	}
}

func waitSmokeMessage(ctx context.Context, updates <-chan pluginv1.Update, runState *smokeRunState, match func(*pluginv1.Message, pluginv1.Update) bool) (*pluginv1.Message, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-runState.done:
			err := runState.error()
			if err == nil {
				return nil, errors.New("live transport stopped before the expected update")
			}
			return nil, err
		case update := <-updates:
			if update.Message != nil && match(update.Message, update) {
				return update.Message, nil
			}
		}
	}
}

func waitSmokeDeletion(ctx context.Context, updates <-chan pluginv1.Update, runState *smokeRunState, messageID int64) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-runState.done:
			err := runState.error()
			if err == nil {
				return errors.New("live transport stopped before the deletion update")
			}
			return err
		case update := <-updates:
			if update.Kind != pluginv1.UpdateDeleted {
				continue
			}
			for _, deletedID := range update.DeletedIDs {
				if deletedID == messageID {
					return nil
				}
			}
		}
	}
}

func smokeMarker() string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "Serenox live smoke fallback"
	}
	return "Serenox live smoke " + hex.EncodeToString(bytes[:])
}

func redactSmokeError(err error, config liveSmokeConfig, marker string) string {
	value := err.Error()
	for _, secret := range []string{config.AppHash, config.SessionPath, marker} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "<redacted>")
		}
	}
	return value
}
