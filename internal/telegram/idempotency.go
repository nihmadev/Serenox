package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	DefaultIdempotencyRetention = 10 * time.Minute
	DefaultIdempotencyCapacity  = 4096
)

// IdempotencyStore is an intentionally in-memory, bounded result journal. It
// is scoped by the caller supplied scope string (normally plugin/account) and
// is cleared on restart. Pending entries also serialize concurrent duplicate
// requests so a timeout cannot cause two callers to send the same mutation.
type IdempotencyStore struct {
	mu        sync.Mutex
	capacity  int
	retention time.Duration
	entries   map[string]*idempotencyEntry
}

type idempotencyEntry struct {
	created time.Time
	ready   chan struct{}
	result  pluginv1.ActionResult
}

func NewIdempotencyStore(capacity int, retention time.Duration) (*IdempotencyStore, error) {
	if capacity == 0 {
		capacity = DefaultIdempotencyCapacity
	}
	if retention == 0 {
		retention = DefaultIdempotencyRetention
	}
	if capacity < 1 || capacity > 1<<16 || retention <= 0 || retention > 24*time.Hour {
		return nil, errors.New("invalid idempotency store limits")
	}
	return &IdempotencyStore{capacity: capacity, retention: retention, entries: make(map[string]*idempotencyEntry, capacity)}, nil
}

// Execute returns the original result for a duplicate key and otherwise runs
// fn exactly once for the key. The result is retained even when it is an
// outcome-unknown failure: callers must use a read/recovery policy instead of
// blindly repeating a Telegram mutation.
func (s *IdempotencyStore) Execute(ctx context.Context, scope string, action pluginv1.Action, fn func() pluginv1.ActionResult) pluginv1.ActionResult {
	if s == nil || action.IdempotencyKey == "" {
		return fn()
	}
	key := scope + "\x00" + action.OperationName() + "\x00" + action.IdempotencyKey
	now := time.Now()
	s.mu.Lock()
	s.evictLocked(now)
	if entry, ok := s.entries[key]; ok {
		ready := entry.ready
		s.mu.Unlock()
		select {
		case <-ready:
			return cloneActionResult(entry.result)
		case <-ctx.Done():
			return unknownActionResult(action, ctx.Err())
		}
	}
	if len(s.entries) >= s.capacity {
		s.evictOldestLocked()
	}
	entry := &idempotencyEntry{created: now, ready: make(chan struct{})}
	s.entries[key] = entry
	s.mu.Unlock()

	result := fn()
	s.mu.Lock()
	entry.result = cloneActionResult(result)
	close(entry.ready)
	s.mu.Unlock()
	return result
}

func (s *IdempotencyStore) evictLocked(now time.Time) {
	for key, entry := range s.entries {
		if now.Sub(entry.created) >= s.retention {
			select {
			case <-entry.ready:
				delete(s.entries, key)
			default:
			}
		}
	}
}

func (s *IdempotencyStore) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range s.entries {
		select {
		case <-entry.ready:
		default:
			continue
		}
		if oldestKey == "" || entry.created.Before(oldest) {
			oldestKey, oldest = key, entry.created
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

func (s *IdempotencyStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(time.Now())
	return len(s.entries)
}

// ForgetScope releases results owned by an unloaded plugin/account.
func (s *IdempotencyStore) ForgetScope(scopePrefix string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.entries {
		if strings.HasPrefix(key, scopePrefix+"\x00") {
			delete(s.entries, key)
		}
	}
}

func (s *IdempotencyStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = make(map[string]*idempotencyEntry, s.capacity)
	s.mu.Unlock()
}

func cloneActionResult(result pluginv1.ActionResult) pluginv1.ActionResult {
	result.Metadata = cloneResultMap(result.Metadata)
	if result.Error != nil {
		errorCopy := *result.Error
		errorCopy.Details = cloneResultMap(result.Error.Details)
		result.Error = &errorCopy
	}
	return result
}

func cloneResultMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func unknownActionResult(action pluginv1.Action, err error) pluginv1.ActionResult {
	return pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultOutcomeUnknown, OutcomeUnknown: true, Error: &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "mutation outcome is unknown", Retryable: false, Details: map[string]string{"cause": boundedError(err)}}}
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > pluginv1.MaxActionMetadataValueBytes {
		return value[:pluginv1.MaxActionMetadataValueBytes]
	}
	return value
}

func actionErrorResult(action pluginv1.Action, err error) pluginv1.ActionResult {
	result := pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultFailure, Success: false}
	if err == nil {
		return result
	}
	var protocolErr *pluginv1.Error
	if errors.As(err, &protocolErr) {
		result.Error = protocolErr
		result.Retryable = protocolErr.Retryable
		if protocolErr.Code == pluginv1.ErrorOutcomeUnknown {
			result.Status, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, true
		}
		return result
	}
	if errors.Is(err, pluginv1.ErrInvalidDTO) {
		result.Status = pluginv1.ActionResultFailure
		result.Error = &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: boundedError(err)}
		return result
	}
	result.Error = &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: boundedError(err)}
	result.Status, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, true
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result.Status, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, true
		result.Error.Code = pluginv1.ErrorOutcomeUnknown
		return result
	}
	if errors.Is(err, ErrActionUnsupported) || errors.Is(err, ErrUnsupportedActionOptions) {
		result.Error.Code, result.Status, result.OutcomeUnknown = pluginv1.ErrorUnsupported, pluginv1.ActionResultFailure, false
	} else if errors.Is(err, ErrPeerNotResolved) {
		result.Error.Code, result.Status, result.OutcomeUnknown = pluginv1.ErrorPeerNotFound, pluginv1.ActionResultFailure, false
	}
	upper := strings.ToUpper(err.Error())
	if strings.Contains(upper, "FLOOD_WAIT") || strings.Contains(upper, "FLOOD WAIT") {
		result.Error.Code, result.Error.Retryable, result.Retryable, result.Status, result.OutcomeUnknown = pluginv1.ErrorFloodWait, true, true, pluginv1.ActionResultFailure, false
		for _, token := range strings.FieldsFunc(err.Error(), func(r rune) bool { return r < '0' || r > '9' }) {
			if seconds, parseErr := strconv.ParseInt(token, 10, 64); parseErr == nil && seconds <= 24*60*60 {
				result.FloodWaitSeconds = seconds
				break
			}
		}
	} else if strings.Contains(upper, "PERMISSION") || strings.Contains(upper, "FORBIDDEN") {
		result.Error.Code, result.Status, result.OutcomeUnknown = pluginv1.ErrorPermissionDenied, pluginv1.ActionResultFailure, false
	} else if strings.Contains(upper, "MESSAGE") && strings.Contains(upper, "NOT FOUND") {
		result.Error.Code, result.Status, result.OutcomeUnknown = pluginv1.ErrorMessageNotFound, pluginv1.ActionResultFailure, false
	}
	return result
}

// ApplyBatchResults applies actions sequentially and returns one honest result
// for every action that was attempted. Later actions run after failures unless
// StopOnError is set. Telegram mutations are never retried here.
func ApplyBatchResults(ctx context.Context, transport Transport, request pluginv1.ActionBatchRequest, scope string, store *IdempotencyStore) (pluginv1.ActionBatchResponse, error) {
	if transport == nil {
		return pluginv1.ActionBatchResponse{}, errors.New("nil action transport")
	}
	if err := request.Validate(); err != nil {
		return pluginv1.ActionBatchResponse{}, err
	}
	response := pluginv1.ActionBatchResponse{Results: make([]pluginv1.ActionResult, 0, len(request.Actions))}
	for _, action := range request.Actions {
		if err := ctx.Err(); err != nil {
			result := unknownActionResult(action, err)
			response.Results = append(response.Results, result)
			response.Partial, response.Stopped = true, true
			break
		}
		apply := func() pluginv1.ActionResult {
			result, err := transport.Apply(ctx, action)
			if result.ActionID == "" {
				result = actionErrorResult(action, err)
			}
			if result.ActionID == "" {
				result.ActionID = action.ID
			}
			if result.Operation == "" {
				result.Operation = action.OperationName()
			}
			if err != nil && result.Error == nil {
				result = actionErrorResult(action, err)
			}
			if result.Status == "" {
				result.Status, result.Success = pluginv1.ActionResultSuccess, true
			}
			return result
		}
		result := pluginv1.ActionResult{}
		if store != nil && action.IdempotencyKey != "" {
			result = store.Execute(ctx, scope, action, apply)
		} else {
			result = apply()
		}
		response.Results = append(response.Results, result)
		if result.Status != pluginv1.ActionResultSuccess && request.StopOnError {
			response.Partial, response.Stopped = true, true
			break
		}
	}
	if len(response.Results) != len(request.Actions) {
		response.Partial = true
	}
	return response, nil
}
