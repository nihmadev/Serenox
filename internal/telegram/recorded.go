package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type RecordedOptions struct {
	MaxUpdates int
	MaxLine    int
	MaxActions int
	// ActionResults provides deterministic per-action fixtures keyed by
	// action ID. It is deliberately DTO-only and credential-free.
	ActionResults          map[string]pluginv1.ActionResult
	TimeoutAfterAcceptance map[string]bool
	// ClientResults is a deterministic DTO-only fixture keyed by explicit read
	// operation. It is never used as evidence of live Telegram support.
	ClientResults map[pluginv1.ClientReadOperation]pluginv1.ClientResult
}

func (o RecordedOptions) normalized() (RecordedOptions, error) {
	if o.MaxUpdates == 0 {
		o.MaxUpdates = 10_000
	}
	if o.MaxLine == 0 {
		o.MaxLine = 1 << 20
	}
	if o.MaxActions == 0 {
		o.MaxActions = 10_000
	}
	if o.MaxUpdates < 1 || o.MaxLine < 1 || o.MaxActions < 1 {
		return RecordedOptions{}, errors.New("invalid recorded transport limits")
	}
	return o, nil
}

// RecordedTransport replays newline-delimited Update DTOs and records actions.
// It is deterministic and never starts a background goroutine.
type RecordedTransport struct {
	options         RecordedOptions
	updates         []pluginv1.Update
	mu              sync.Mutex
	actions         []pluginv1.Action
	results         map[string]pluginv1.ActionResult
	closed          bool
	readService     *ReadService
	clientResults   map[pluginv1.ClientReadOperation]pluginv1.ClientResult
	moderation      *ModerationService
	moderationState *recordedModerationState
	profile         *pluginv1.Peer
}

func LoadRecorded(path string, options RecordedOptions) (*RecordedTransport, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recorded updates: %w", err)
	}
	defer file.Close()
	return ReadRecorded(file, options)
}

func ReadRecorded(reader io.Reader, options RecordedOptions) (*RecordedTransport, error) {
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), normalized.MaxLine)
	updates := make([]pluginv1.Update, 0, min(normalized.MaxUpdates, 256))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if len(updates) >= normalized.MaxUpdates {
			return nil, errors.New("recorded update limit exceeded")
		}
		var update pluginv1.Update
		if err := json.Unmarshal(line, &update); err != nil {
			return nil, fmt.Errorf("decode recorded update %d: %w", len(updates)+1, err)
		}
		if err := update.Validate(); err != nil {
			return nil, fmt.Errorf("validate recorded update %d: %w", len(updates)+1, err)
		}
		updates = append(updates, update)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan recorded updates: %w", err)
	}
	secret := []byte("serenox-recorded-read-cursor-secret-32")
	cursor, err := NewCursorCodec(secret, time.Duration(pluginv1.MaxClientCursorLifetimeSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	transport := &RecordedTransport{options: normalized, updates: updates, results: make(map[string]pluginv1.ActionResult), clientResults: cloneClientResults(normalized.ClientResults)}
	if identity := transport.clientResults[pluginv1.ClientGetMe].Me; identity != nil {
		copy := *identity
		transport.profile = &copy
	}
	transport.readService, err = NewReadService(transport, cursor, 4)
	if err != nil {
		return nil, err
	}
	transport.moderationState = newRecordedModerationState()
	transport.moderation, err = NewModerationService(transport, 4, 1024)
	if err != nil {
		return nil, err
	}
	return transport, nil
}

func (t *RecordedTransport) Run(ctx context.Context, sink Sink) error {
	if sink == nil {
		return errors.New("nil recorded update sink")
	}
	for _, update := range t.updates {
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return ErrClosed
		}
		if err := sink.HandleUpdate(ctx, update); err != nil {
			return normalizeRunError(err)
		}
	}
	return nil
}

func (t *RecordedTransport) Apply(_ context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return pluginv1.ActionResult{}, ErrClosed
	}
	if len(t.actions) >= t.options.MaxActions {
		return pluginv1.ActionResult{}, ErrActionLimit
	}
	if action.IdempotencyKey != "" {
		key := action.OperationName() + "\x00" + action.IdempotencyKey
		if result, ok := t.results[key]; ok {
			return result, nil
		}
	}
	t.actions = append(t.actions, action)
	result := t.options.ActionResults[action.ID]
	if result.ActionID != "" && result.ActionID != action.ID {
		return pluginv1.ActionResult{}, fmt.Errorf("recorded action result ID %q does not match action %q", result.ActionID, action.ID)
	}
	if result.ActionID == "" {
		result = pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultSuccess, Success: true}
	}
	if result.Operation == "" {
		result.Operation = action.OperationName()
	}
	if result.Status == "" {
		result.Status = pluginv1.ActionResultSuccess
		result.Success = true
	}
	if t.options.TimeoutAfterAcceptance[action.ID] {
		result.Status, result.Success, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, false, true
		result.Error = &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "recorded timeout after server acceptance"}
	}
	if err := result.Validate(); err != nil {
		return pluginv1.ActionResult{}, err
	}
	if action.IdempotencyKey != "" {
		t.results[action.OperationName()+"\x00"+action.IdempotencyKey] = result
	}
	if t.options.TimeoutAfterAcceptance[action.ID] {
		return result, context.DeadlineExceeded
	}
	return result, nil
}

func (t *RecordedTransport) Actions() []pluginv1.Action {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]pluginv1.Action(nil), t.actions...)
}

func (t *RecordedTransport) Close(context.Context) error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	if t.moderation != nil {
		t.moderation.Close()
	}
	return nil
}

func (t *RecordedTransport) Read(ctx context.Context, scope ReadScope, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	return t.readService.Read(ctx, scope, request)
}

func (t *RecordedTransport) GetProfileIdentity(context.Context) (*pluginv1.Peer, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	if t.profile == nil {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "recorded profile identity fixture is unavailable"}
	}
	copy := *t.profile
	return &copy, nil
}

func (t *RecordedTransport) ApplyProfileName(ctx context.Context, action pluginv1.Action) (*pluginv1.Peer, error) {
	t.mu.Lock()
	t.actions = append(t.actions, action)
	t.mu.Unlock()
	return t.UpdateProfileName(ctx, action.Profile.FirstName, action.Profile.LastName)
}

func (t *RecordedTransport) UpdateProfileName(_ context.Context, firstName, lastName string) (*pluginv1.Peer, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	if err := (pluginv1.ProfileNameAction{Operation: pluginv1.ProfileUpdate, FirstName: firstName, LastName: lastName, Generation: 1, LeaseToken: "profile"}).Validate(); err != nil {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorProfileInvalidName, Message: "recorded profile name is invalid"}
	}
	if t.profile == nil {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "recorded profile identity fixture is unavailable"}
	}
	t.profile.FirstName, t.profile.LastName = firstName, lastName
	copy := *t.profile
	return &copy, nil
}

func (t *RecordedTransport) Moderate(ctx context.Context, scope ModerationScope, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
	return t.moderation.Moderate(ctx, scope, request)
}

func (t *RecordedTransport) fixture(operation pluginv1.ClientReadOperation) (pluginv1.ClientResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	value, ok := t.clientResults[operation]
	if !ok {
		return pluginv1.ClientResult{}, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Operation: string(operation), Message: "recorded read fixture is unavailable"}
	}
	return cloneClientResult(value), nil
}

func (t *RecordedTransport) ReadFixture(_ context.Context, request pluginv1.ClientRequest) (pluginv1.ClientResult, bool, error) {
	t.mu.Lock()
	value, ok := t.clientResults[request.Operation]
	t.mu.Unlock()
	if !ok {
		return pluginv1.ClientResult{}, false, nil
	}
	return cloneClientResult(value), true, nil
}

func (t *RecordedTransport) GetMe(context.Context) (*pluginv1.Peer, error) {
	value, err := t.fixture(pluginv1.ClientGetMe)
	return value.Me, err
}
func (t *RecordedTransport) ResolvePeer(_ context.Context, peerID int64, username string) (*pluginv1.Peer, error) {
	value, err := t.fixture(pluginv1.ClientResolvePeer)
	if err != nil {
		return nil, err
	}
	if value.Peer == nil {
		return nil, errors.New("recorded peer is missing")
	}
	return value.Peer, nil
}
func (t *RecordedTransport) GetMessage(_ context.Context, peerID, messageID int64) (*pluginv1.Message, error) {
	value, err := t.fixture(pluginv1.ClientGetMessage)
	if err != nil {
		return nil, err
	}
	for i := range value.Messages {
		if value.Messages[i].ChatID == peerID && value.Messages[i].ID == messageID {
			return &value.Messages[i], nil
		}
	}
	if value.Message != nil && value.Message.ChatID == peerID && value.Message.ID == messageID {
		return value.Message, nil
	}
	return nil, nil
}
func (t *RecordedTransport) GetMessages(_ context.Context, peerID int64, messageIDs []int64) ([]pluginv1.Message, error) {
	value, err := t.fixture(pluginv1.ClientGetMessages)
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64]struct{}, len(messageIDs))
	for _, id := range messageIDs {
		allowed[id] = struct{}{}
	}
	result := make([]pluginv1.Message, 0, len(messageIDs))
	for _, message := range value.Messages {
		if message.ChatID == peerID {
			if _, ok := allowed[message.ID]; ok {
				result = append(result, message)
			}
		}
	}
	return result, nil
}
func (t *RecordedTransport) GetHistory(_ context.Context, peerID, offsetID int64, limit int, _ string) ([]pluginv1.Message, bool, error) {
	value, err := t.fixture(pluginv1.ClientGetHistory)
	if err != nil {
		return nil, false, err
	}
	messages, _ := pageMessages(value.Messages, peerID, offsetID, limit)
	return messages, value.Page != nil && value.Page.HasMore, nil
}
func (t *RecordedTransport) SearchMessages(_ context.Context, peerID int64, query string, senderID int64, mediaKind string, offsetID int64, limit int) ([]pluginv1.Message, bool, error) {
	value, err := t.fixture(pluginv1.ClientSearchMessages)
	if err != nil {
		return nil, false, err
	}
	messages, _ := pageMessages(value.Messages, peerID, offsetID, limit)
	return messages, value.Page != nil && value.Page.HasMore, nil
}

func cloneClientResults(values map[pluginv1.ClientReadOperation]pluginv1.ClientResult) map[pluginv1.ClientReadOperation]pluginv1.ClientResult {
	result := make(map[pluginv1.ClientReadOperation]pluginv1.ClientResult, len(values))
	for key, value := range values {
		result[key] = cloneClientResult(value)
	}
	return result
}
func cloneClientResult(value pluginv1.ClientResult) pluginv1.ClientResult {
	result := value
	result.Messages = append([]pluginv1.Message(nil), value.Messages...)
	result.Users = append([]pluginv1.User(nil), value.Users...)
	result.Chats = append([]pluginv1.Chat(nil), value.Chats...)
	return result
}
func pageMessages(values []pluginv1.Message, peerID, offsetID int64, limit int) ([]pluginv1.Message, bool) {
	result := make([]pluginv1.Message, 0, limit)
	for _, value := range values {
		if value.ChatID != peerID || (offsetID != 0 && value.ID >= offsetID) {
			continue
		}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result, len(result) == limit && len(values) > len(result)
}
