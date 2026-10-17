// Package controlbot owns the optional Bot API control surface. It is not a
// Telegram user-session transport and exposes only Serenox protocol DTOs.
package controlbot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	secretFile = "control-bot.token"
	offsetFile = "control-bot.offset"
	maxBody    = 1 << 20
)

var (
	ErrNotAttached    = errors.New("control bot is not attached")
	ErrUnauthorized   = errors.New("control bot token is unauthorized")
	ErrConflict       = errors.New("control bot update stream is owned by another instance")
	ErrMalformed      = errors.New("malformed Bot API response")
	ErrOutcomeUnknown = errors.New("bot API mutation outcome is unknown")
	tokenPattern      = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,128}$`)
)

// Identity is the safe verified subset returned by getMe.
type Identity struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

// Status intentionally has no token, URL, query, actor, or chat information.
type Status struct {
	Attached     bool     `json:"attached"`
	Running      bool     `json:"running"`
	Bot          Identity `json:"bot,omitempty"`
	Offset       int64    `json:"offset,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	RestartCount int      `json:"restart_count,omitempty"`
}

type APIError struct {
	Code      pluginv1.ErrorCode
	Retryable bool
	FloodWait time.Duration
	Err       error
}

func (e *APIError) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return e.Err.Error()
}
func (e *APIError) Unwrap() error { return e.Err }

// SecretStore persists one account-bound token in a dedicated mode-0600 file.
// Its atomic replacement guarantees a failed verification never overwrites a
// working attachment because Verify runs before Save.
type SecretStore struct{ dir string }

func NewSecretStore(dataDir string) (*SecretStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("control bot data directory is required")
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	return &SecretStore{dir: dir}, nil
}
func (s *SecretStore) Path() string       { return filepath.Join(s.dir, secretFile) }
func (s *SecretStore) OffsetPath() string { return filepath.Join(s.dir, offsetFile) }
func ValidateToken(token string) error {
	if !tokenPattern.MatchString(strings.TrimSpace(token)) {
		return errors.New("control bot token has invalid format")
	}
	return nil
}
func (s *SecretStore) Load() (string, error) {
	raw, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotAttached
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if err := ValidateToken(token); err != nil {
		return "", fmt.Errorf("stored control bot token: %w", err)
	}
	return token, nil
}
func (s *SecretStore) Save(token string) error {
	if err := ValidateToken(token); err != nil {
		return err
	}
	return atomicPrivateFile(s.Path(), []byte(strings.TrimSpace(token)+"\n"))
}
func (s *SecretStore) Delete() error {
	err := os.Remove(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *SecretStore) LoadOffset() (int64, error) {
	raw, err := os.ReadFile(s.OffsetPath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	result, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || result < 0 {
		return 0, errors.New("invalid control bot polling offset")
	}
	return result, nil
}
func (s *SecretStore) SaveOffset(offset int64) error {
	if offset < 0 {
		return errors.New("negative control bot offset")
	}
	return atomicPrivateFile(s.OffsetPath(), []byte(strconv.FormatInt(offset, 10)+"\n"))
}
func (s *SecretStore) ClearOffset() error {
	err := os.Remove(s.OffsetPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func atomicPrivateFile(path string, value []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".control-bot-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// Client is a bounded, core-owned Bot API adapter. baseURL exists only for
// deterministic fakes; plugins are never given a Client.
type Client struct {
	token, baseURL string
	http           *http.Client
	maxBody        int64
}
type ClientOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	MaxBody    int64
}

func NewClient(token string, options ClientOptions) (*Client, error) {
	if err := ValidateToken(token); err != nil {
		return nil, err
	}
	if options.BaseURL == "" {
		options.BaseURL = "https://api.telegram.org"
	}
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || parsed.Scheme != "https" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		return nil, errors.New("invalid Bot API base URL")
	}
	if options.HTTPClient == nil {
		// getUpdates is a 25-second long-poll. The client deadline must exceed
		// it or every healthy idle poll is canceled before Telegram can reply.
		options.HTTPClient = &http.Client{Timeout: 35 * time.Second}
	}
	if options.MaxBody == 0 {
		options.MaxBody = maxBody
	}
	if options.MaxBody < 1024 || options.MaxBody > 8<<20 {
		return nil, errors.New("invalid Bot API response limit")
	}
	return &Client{token: token, baseURL: strings.TrimRight(options.BaseURL, "/"), http: options.HTTPClient, maxBody: options.MaxBody}, nil
}
func (c *Client) endpoint(method string) string { return c.baseURL + "/bot" + c.token + "/" + method }

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) call(ctx context.Context, method string, payload any, mutation bool, output any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(encoded) > int(c.maxBody) {
		return errors.New("bot API request exceeds bounded payload limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &APIError{Code: pluginv1.ErrorBotTimeout, Retryable: !mutation, Err: ctx.Err()}
		}
		if mutation {
			return &APIError{Code: pluginv1.ErrorOutcomeUnknown, Err: ErrOutcomeUnknown}
		}
		return &APIError{Code: pluginv1.ErrorBotNetwork, Retryable: true, Err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, c.maxBody+1))
	if err != nil {
		return &APIError{Code: pluginv1.ErrorBotNetwork, Retryable: !mutation, Err: err}
	}
	if int64(len(raw)) > c.maxBody {
		return &APIError{Code: pluginv1.ErrorBotMalformedResponse, Err: ErrMalformed}
	}
	var envelope apiEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return &APIError{Code: pluginv1.ErrorBotMalformedResponse, Err: ErrMalformed}
	}
	if !envelope.OK {
		apiErr := &APIError{Code: pluginv1.ErrorInternal, Err: errors.New("bot API rejected request")}
		switch envelope.ErrorCode {
		case 401:
			apiErr.Code = pluginv1.ErrorBotUnauthorized
			apiErr.Err = ErrUnauthorized
		case 409:
			apiErr.Code = pluginv1.ErrorBotConflict
			apiErr.Err = ErrConflict
		case 429:
			apiErr.Code = pluginv1.ErrorFloodWait
			apiErr.Retryable = true
			apiErr.FloodWait = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		return apiErr
	}
	if output != nil && json.Unmarshal(envelope.Result, output) != nil {
		return &APIError{Code: pluginv1.ErrorBotMalformedResponse, Err: ErrMalformed}
	}
	return nil
}
func (c *Client) GetMe(ctx context.Context) (Identity, error) {
	var result struct {
		ID       int64  `json:"id"`
		IsBot    bool   `json:"is_bot"`
		Username string `json:"username"`
	}
	if err := c.call(ctx, "getMe", struct{}{}, false, &result); err != nil {
		return Identity{}, err
	}
	if result.ID == 0 || !result.IsBot || result.Username == "" {
		return Identity{}, &APIError{Code: pluginv1.ErrorBotMalformedResponse, Err: ErrMalformed}
	}
	return Identity{ID: result.ID, Username: result.Username}, nil
}

type rawUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type rawMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From rawUser `json:"from"`
	Text string  `json:"text"`
	Date int64   `json:"date"`
}
type rawCallback struct {
	ID              string      `json:"id"`
	From            rawUser     `json:"from"`
	Data            string      `json:"data"`
	InlineMessageID string      `json:"inline_message_id"`
	ChatInstance    string      `json:"chat_instance"`
	Message         *rawMessage `json:"message"`
}
type rawInline struct {
	ID       string  `json:"id"`
	From     rawUser `json:"from"`
	Query    string  `json:"query"`
	Offset   string  `json:"offset"`
	ChatType string  `json:"chat_type"`
}
type rawUpdate struct {
	UpdateID int64        `json:"update_id"`
	Message  *rawMessage  `json:"message"`
	Callback *rawCallback `json:"callback_query"`
	Inline   *rawInline   `json:"inline_query"`
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]rawUpdate, error) {
	if offset < 0 || timeoutSeconds < 0 || timeoutSeconds > 50 {
		return nil, errors.New("invalid control-bot polling request")
	}
	var result []rawUpdate
	err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeoutSeconds, "allowed_updates": []string{"message", "callback_query", "inline_query"}}, false, &result)
	return result, err
}

func convertUpdate(identity Identity, update rawUpdate) (pluginv1.Update, bool) {
	id := strconv.FormatInt(update.UpdateID, 10)
	base := pluginv1.Update{ID: id, OccurredUnixMS: time.Now().UTC().UnixMilli()}
	if update.Inline != nil {
		q := update.Inline
		base.Kind = pluginv1.UpdateControlInlineQuery
		base.Control = &pluginv1.ControlEvent{Bot: pluginv1.ControlBotIdentity{ID: identity.ID, Username: identity.Username}, Actor: pluginv1.ControlActor{ID: q.From.ID, Username: q.From.Username}, QueryID: q.ID, Query: q.Query, Offset: q.Offset, ChatType: q.ChatType}
		return base, base.Validate() == nil
	}
	if update.Callback != nil {
		q := update.Callback
		e := &pluginv1.ControlEvent{Bot: pluginv1.ControlBotIdentity{ID: identity.ID, Username: identity.Username}, Actor: pluginv1.ControlActor{ID: q.From.ID, Username: q.From.Username}, QueryID: q.ID, CallbackData: q.Data, InlineMessageID: q.InlineMessageID, ChatInstance: q.ChatInstance}
		if q.Message != nil {
			e.ChatID = q.Message.Chat.ID
			e.MessageID = q.Message.MessageID
		}
		base.Kind = pluginv1.UpdateControlCallback
		base.Control = e
		return base, base.Validate() == nil
	}
	if update.Message != nil {
		m := update.Message
		command, args, ok := parseCommand(m.Text, identity.Username)
		if !ok || m.Chat.Type != "private" {
			return pluginv1.Update{}, false
		}
		base.Kind = pluginv1.UpdateControlMessage
		base.OccurredUnixMS = m.Date * 1000
		base.Control = &pluginv1.ControlEvent{Bot: pluginv1.ControlBotIdentity{ID: identity.ID, Username: identity.Username}, Actor: pluginv1.ControlActor{ID: m.From.ID, Username: m.From.Username}, ChatID: m.Chat.ID, MessageID: m.MessageID, Command: command, CommandArguments: args}
		return base, base.Validate() == nil
	}
	return pluginv1.Update{}, false
}
func parseCommand(text, username string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		if !strings.EqualFold(name[at+1:], username) {
			return "", "", false
		}
		name = name[:at]
	}
	if name == "" {
		return "", "", false
	}
	args := ""
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	}
	return strings.ToLower(name), args, true
}

// Poller has one long-poll loop and a persisted next offset. Updating the
// offset only after sink success provides at-least-once recovery; command and
// action idempotency is intentionally handled above user-session mutations.
type Sink interface {
	HandleControlUpdate(context.Context, pluginv1.Update) error
}
type SinkFunc func(context.Context, pluginv1.Update) error

func (f SinkFunc) HandleControlUpdate(ctx context.Context, u pluginv1.Update) error { return f(ctx, u) }

type Poller struct {
	client   *Client
	store    *SecretStore
	identity Identity
	mu       sync.RWMutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	offset   int64
	lastErr  string
	restarts int
}

func NewPoller(client *Client, store *SecretStore, identity Identity) (*Poller, error) {
	if client == nil || store == nil || identity.ID == 0 {
		return nil, errors.New("control bot poller requires client, store, and identity")
	}
	offset, err := store.LoadOffset()
	if err != nil {
		return nil, err
	}
	return &Poller{client: client, store: store, identity: identity, offset: offset}, nil
}
func (p *Poller) Start(parent context.Context, sink Sink) error {
	if sink == nil {
		return errors.New("control bot sink is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return errors.New("control bot poller already started")
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.done = make(chan struct{})
	p.running = true
	go p.run(ctx, sink)
	return nil
}
func (p *Poller) run(ctx context.Context, sink Sink) {
	defer func() { p.mu.Lock(); p.running = false; p.mu.Unlock(); close(p.done) }()
	backoff := time.Second
	for {
		p.mu.RLock()
		offset := p.offset
		p.mu.RUnlock()
		updates, err := p.client.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.mu.Lock()
			p.lastErr = errorClass(err)
			p.restarts++
			p.mu.Unlock()
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, raw := range updates {
			next := raw.UpdateID + 1
			if raw.UpdateID < offset {
				continue
			}
			update, ok := convertUpdate(p.identity, raw)
			if ok {
				if err := sink.HandleControlUpdate(ctx, update); err != nil {
					p.mu.Lock()
					p.lastErr = updateErrorClass(err)
					p.restarts++
					p.mu.Unlock()
					// A malformed or rejected individual control update must not
					// take down the entire bot indefinitely. Its offset is still
					// committed below, so it cannot poison every later poll.
				}
			}
			if err := p.store.SaveOffset(next); err != nil {
				p.mu.Lock()
				p.lastErr = "offset"
				p.mu.Unlock()
				return
			}
			p.mu.Lock()
			p.offset = next
			p.mu.Unlock()
			offset = next
		}
	}
}
func (p *Poller) Stop(ctx context.Context) error {
	p.mu.RLock()
	cancel, done := p.cancel, p.done
	p.mu.RUnlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Poller) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Status{Attached: true, Running: p.running, Bot: p.identity, Offset: p.offset, LastError: p.lastErr, RestartCount: p.restarts}
}
func errorClass(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		return string(api.Code)
	}
	return "network"
}

func updateErrorClass(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		return string(api.Code)
	}
	return "update"
}

// Manager serializes attachment, rotation, polling, and detachment. A caller
// supplies its own account data directory, making one secret file correspond
// to exactly one configured account instance. A token is verified before it is
// committed; on a failed post-rotation start the previous verified attachment
// is restored and restarted when possible.
type Manager struct {
	store       *SecretStore
	options     ClientOptions
	mu          sync.Mutex
	client      *Client
	identity    Identity
	poller      *Poller
	sink        Sink
	pollContext context.Context
}

func NewManager(store *SecretStore, options ClientOptions) (*Manager, error) {
	if store == nil {
		return nil, errors.New("control bot secret store is required")
	}
	return &Manager{store: store, options: options}, nil
}

func (m *Manager) Attach(ctx context.Context, token string) (Identity, error) {
	if err := ValidateToken(token); err != nil {
		return Identity{}, err
	}
	// Do all fallible remote work before touching the durable secret.
	candidate, err := NewClient(token, m.options)
	if err != nil {
		return Identity{}, err
	}
	identity, err := candidate.GetMe(ctx)
	if err != nil {
		return Identity{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previousToken, previousErr := m.store.Load()
	previousClient, previousIdentity, previousPoller := m.client, m.identity, m.poller
	wasRunning := previousPoller != nil
	sink, pollContext := m.sink, m.pollContext
	if wasRunning {
		if err := previousPoller.Stop(ctx); err != nil {
			return Identity{}, err
		}
	}
	if err := m.store.Save(token); err != nil {
		if wasRunning {
			_ = previousPoller.Start(pollContext, sink)
		}
		return Identity{}, err
	}
	m.client, m.identity, m.poller = candidate, identity, nil
	if wasRunning {
		poller, startErr := NewPoller(candidate, m.store, identity)
		if startErr == nil {
			startErr = poller.Start(pollContext, sink)
		}
		if startErr == nil {
			m.poller = poller
			return identity, nil
		}
		// A failed activation is rolled back to the last known verified token.
		if previousErr == nil {
			_ = m.store.Save(previousToken)
		}
		m.client, m.identity, m.poller = previousClient, previousIdentity, nil
		if previousClient != nil && previousErr == nil {
			if restored, restoreErr := NewPoller(previousClient, m.store, previousIdentity); restoreErr == nil {
				if restoreErr = restored.Start(pollContext, sink); restoreErr == nil {
					m.poller = restored
				}
			}
		}
		return Identity{}, fmt.Errorf("activate rotated control bot: %w", startErr)
	}
	return identity, nil
}

func (m *Manager) Start(ctx context.Context, sink Sink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poller != nil {
		if m.poller.Status().Running {
			return errors.New("control bot already started")
		}
		// Poller can have terminated after an unrecoverable local failure.
		// It has no goroutine left at this point, so discard the stale handle
		// and allow an explicit reload to recover without a daemon restart.
		m.poller = nil
	}
	if m.client == nil {
		token, err := m.store.Load()
		if err != nil {
			return err
		}
		client, err := NewClient(token, m.options)
		if err != nil {
			return err
		}
		identity, err := client.GetMe(ctx)
		if err != nil {
			return err
		}
		m.client, m.identity = client, identity
	}
	poller, err := NewPoller(m.client, m.store, m.identity)
	if err != nil {
		return err
	}
	if err := poller.Start(ctx, sink); err != nil {
		return err
	}
	m.poller, m.sink, m.pollContext = poller, sink, ctx
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	poller := m.poller
	m.poller = nil
	m.sink = nil
	m.pollContext = nil
	m.mu.Unlock()
	if poller == nil {
		return nil
	}
	return poller.Stop(ctx)
}

func (m *Manager) Detach(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client, m.identity = nil, Identity{}
	if err := m.store.ClearOffset(); err != nil {
		return err
	}
	return m.store.Delete()
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poller != nil {
		return m.poller.Status()
	}
	if m.client != nil {
		return Status{Attached: true, Bot: m.identity}
	}
	if _, err := m.store.Load(); err == nil {
		return Status{Attached: true}
	}
	return Status{}
}

// Apply is the sole app-facing Bot API action boundary. It makes attachment
// state explicit and keeps the token/client encapsulated in this package.
func (m *Manager) Apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	if client == nil {
		return pluginv1.ActionResult{}, ErrNotAttached
	}
	return client.Apply(ctx, action)
}

// Apply maps the finite protocol action subset to Bot API methods. No generic
// method or arbitrary JSON escape hatch is available to a plugin.
func (c *Client) Apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, err
	}
	if action.ResponseChannel != pluginv1.ResponseChannelControlBot {
		return pluginv1.ActionResult{}, errors.New("user-session action cannot use control bot")
	}
	result := pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultSuccess, Success: true}
	var method string
	payload := map[string]any{}
	switch action.Kind {
	case pluginv1.ActionBotAnswerInline:
		method = "answerInlineQuery"
		payload["inline_query_id"] = action.InlineQueryID
		results, convertErr := botInlineResults(action.InlineResults)
		if convertErr != nil {
			return pluginv1.ActionResult{}, convertErr
		}
		payload["results"] = results
		payload["cache_time"] = action.CacheTimeSeconds
		payload["is_personal"] = action.IsPersonal
		payload["next_offset"] = action.NextOffset
	case pluginv1.ActionBotAnswerCallback:
		method = "answerCallbackQuery"
		payload["callback_query_id"] = action.CallbackID
		payload["text"] = action.CallbackText
		payload["show_alert"] = action.ShowAlert
	case pluginv1.ActionBotSend:
		method = "sendMessage"
		payload["chat_id"] = action.ChatID
		payload["text"] = action.Text
		payload["parse_mode"] = action.ParseMode
		payload["reply_markup"] = botMarkup(action.Buttons)
	case pluginv1.ActionBotEdit:
		method = "editMessageText"
		payload["text"] = action.Text
		payload["parse_mode"] = action.ParseMode
		payload["reply_markup"] = botMarkup(action.Buttons)
		if action.InlineMessageID != "" {
			payload["inline_message_id"] = action.InlineMessageID
		} else {
			payload["chat_id"] = action.ChatID
			payload["message_id"] = action.MessageID
		}
	case pluginv1.ActionBotDelete:
		method = "deleteMessage"
		payload["chat_id"] = action.ChatID
		payload["message_id"] = action.MessageID
	default:
		return pluginv1.ActionResult{}, errors.New("unsupported control-bot action")
	}
	var message rawMessage
	mutation := action.Kind != pluginv1.ActionBotAnswerInline && action.Kind != pluginv1.ActionBotAnswerCallback
	err := c.call(ctx, method, payload, mutation, func() any {
		if mutation {
			return &message
		}
		return nil
	}())
	if err != nil {
		result.Success = false
		result.Status = pluginv1.ActionResultFailure
		var api *APIError
		if errors.As(err, &api) {
			result.Retryable = api.Retryable
			result.FloodWaitSeconds = int64(api.FloodWait / time.Second)
			result.Error = &pluginv1.Error{Code: api.Code, Message: "control-bot action failed"}
			if api.Code == pluginv1.ErrorOutcomeUnknown {
				result.Status = pluginv1.ActionResultOutcomeUnknown
				result.OutcomeUnknown = true
			}
		}
		return result, err
	}
	if message.MessageID != 0 {
		result.MessageID = message.MessageID
	}
	return result, nil
}

func botMarkup(rows [][]pluginv1.InlineButton) map[string]any {
	if len(rows) == 0 {
		return nil
	}
	keyboard := make([][]map[string]string, len(rows))
	for rowIndex, row := range rows {
		keyboard[rowIndex] = make([]map[string]string, len(row))
		for buttonIndex, button := range row {
			item := map[string]string{"text": button.Text}
			switch {
			case button.CallbackData != "":
				item["callback_data"] = button.CallbackData
			case button.URL != "":
				item["url"] = button.URL
			case button.SwitchQuery != "":
				item["switch_inline_query"] = button.SwitchQuery
			}
			keyboard[rowIndex][buttonIndex] = item
		}
	}
	return map[string]any{"inline_keyboard": keyboard}
}

func botInlineResults(results []pluginv1.InlineResult) ([]map[string]any, error) {
	converted := make([]map[string]any, len(results))
	for index, result := range results {
		// The v1 Bot API bridge intentionally supports articles only until each
		// media result has an explicit, tested protocol mapping.
		if result.Type != "article" {
			return nil, fmt.Errorf("%w: control-bot inline result type %q; supported type is article", pluginv1.ErrInvalidDTO, result.Type)
		}
		if result.Text == "" {
			return nil, fmt.Errorf("%w: control-bot article text is required", pluginv1.ErrInvalidDTO)
		}
		item := map[string]any{"type": "article", "id": result.ID, "title": result.Title, "input_message_content": map[string]any{"message_text": result.Text}}
		if result.Description != "" {
			item["description"] = result.Description
		}
		if markup := botMarkup(result.Buttons); markup != nil {
			item["reply_markup"] = markup
		}
		converted[index] = item
	}
	return converted, nil
}

// NewOpaqueID provides opaque flow/callback identifiers which are unrelated to
// Telegram message IDs. It is exported for core flow registry implementations.
func NewOpaqueID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("cb_%x", b), nil
}
