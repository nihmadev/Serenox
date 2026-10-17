package telegram

import (
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/ratelimit"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const moderationResultRetention = 10 * time.Minute

type moderationReplay struct {
	key    string
	at     time.Time
	result pluginv1.ModerationResult
}

type ModerationService struct {
	backend    ModerationBackend
	sem        chan struct{}
	lifecycle  context.Context
	cancel     context.CancelFunc
	rate       *ratelimit.Limiter
	ordering   *moderationOrdering
	fairness   *moderationFairness
	counters   moderationCounters
	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        *list.List
	maxEntries int
	clock      func() time.Time
	cursor     *CursorCodec
	closed     bool
}

func NewModerationService(backend ModerationBackend, maxConcurrent, maxEntries int) (*ModerationService, error) {
	if backend == nil {
		return nil, errors.New("moderation service requires a backend")
	}
	if maxConcurrent == 0 {
		maxConcurrent = 16
	}
	if maxEntries == 0 {
		maxEntries = 4096
	}
	if maxConcurrent < 1 || maxConcurrent > 64 || maxEntries < 1 || maxEntries > 65536 {
		return nil, errors.New("moderation service limits are outside bounds")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("create moderation cursor secret: %w", err)
	}
	cursor, err := NewCursorCodec(secret, time.Duration(pluginv1.MaxClientCursorLifetimeSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	rate, err := newModerationRateLimiter()
	if err != nil {
		return nil, err
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &ModerationService{
		backend: backend, sem: make(chan struct{}, maxConcurrent), lifecycle: lifecycle, cancel: cancel,
		rate: rate, ordering: newModerationOrdering(), fairness: newModerationFairness(), entries: make(map[string]*list.Element, maxEntries),
		lru: list.New(), maxEntries: maxEntries, clock: time.Now, cursor: cursor,
	}, nil
}

func (s *ModerationService) Moderate(parent context.Context, scope ModerationScope, request pluginv1.ModerationRequest) (result pluginv1.ModerationResult, err error) {
	observe := s.counters.begin()
	defer func() { observe(result, err) }()
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return moderationFailure(request, pluginv1.ErrorDisconnected, "moderation service is closed", true)
	}
	if err := request.Validate(); err != nil {
		return moderationFailure(request, pluginv1.ErrorInvalidRequest, err.Error(), false)
	}
	if request.Restriction != nil && !request.Restriction.Permanent {
		if err := validateModerationDeadline(s.clock(), request.Restriction.UntilUnixMS); err != nil {
			return mapModerationError(request, err)
		}
	}
	if request.Invite != nil && request.Invite.ExpireUnixMS != nil {
		if err := validateModerationDeadline(s.clock(), *request.Invite.ExpireUnixMS); err != nil {
			return mapModerationError(request, err)
		}
	}
	if scope.PluginID == "" || scope.AccountID == 0 || scope.Permission != pluginv1.ModerationPermission(request.Operation) {
		return moderationFailure(request, pluginv1.ErrorPermissionDenied, "moderation scope or permission is missing", false)
	}
	decision := s.rate.AllowN(moderationRateIdentity(scope, request), moderationRateCost(request))
	if !decision.Allowed {
		result, err := moderationFailure(request, pluginv1.ErrorRateLimited, "moderation rate limit exceeded", true)
		seconds := int64((decision.RetryAfter + time.Second - 1) / time.Second)
		result.Error.FloodWaitSeconds = seconds
		return result, err
	}
	key := moderationReplayKey(scope, request)
	if key != "" {
		if result, ok := s.replay(key); ok {
			result.IdempotentReplay = true
			if result.Error != nil {
				return result, result.Error
			}
			return result, nil
		}
	}
	ctx, cancel := moderationContext(parent)
	defer cancel()
	stopLifecycle := context.AfterFunc(s.lifecycle, cancel)
	defer stopLifecycle()
	if s.lifecycle.Err() != nil {
		cancel()
	}
	releaseScope, fairnessErr := s.fairness.acquire(scope)
	if fairnessErr != nil {
		return mapModerationError(request, fairnessErr)
	}
	defer releaseScope()
	if orderingKey := moderationOrderingKey(scope, request); orderingKey != "" {
		release, acquireErr := s.ordering.acquire(ctx, orderingKey)
		if acquireErr != nil {
			if errors.Is(acquireErr, context.Canceled) || errors.Is(acquireErr, context.DeadlineExceeded) {
				return moderationPreflightContextResult(request, acquireErr)
			}
			return mapModerationError(request, acquireErr)
		}
		defer release()
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return moderationPreflightContextResult(request, ctx.Err())
	}
	if err := s.position(scope, &request); err != nil {
		return moderationFailure(request, pluginv1.ErrorCursorInvalid, "moderation cursor is invalid or out of scope", false)
	}
	if request.ExpectedState != "" && request.Participant != nil {
		member, err := s.backend.ModerationGetMember(ctx, request.Chat, *request.Participant)
		if err != nil {
			return mapModerationError(request, err)
		}
		if memberStateToken(*member) != request.ExpectedState {
			return moderationFailure(request, pluginv1.ErrorModerationStateConflict, "participant state changed since it was inspected", false)
		}
	}
	result, err = s.dispatch(ctx, request)
	if err != nil {
		mapped, mappedErr := mapModerationError(request, err)
		if key != "" && (mapped.Status == pluginv1.ActionResultOutcomeUnknown || mappedErr == nil) {
			s.remember(key, mapped)
		}
		return mapped, mappedErr
	}
	if result.Status == "" {
		result.Status = pluginv1.ActionResultSuccess
	}
	if result.Operation == "" {
		result.Operation, result.RequestID, result.ChatID = request.Operation, request.RequestID, request.Chat.ChatID
	}
	if result.Member != nil {
		result.Member.StateToken = memberStateToken(*result.Member)
	}
	for index := range result.Members {
		result.Members[index].StateToken = memberStateToken(result.Members[index])
	}
	if status, ok := s.backend.(moderationSupportBackend); ok {
		result.Support = &pluginv1.ModerationSupport{ChatKind: request.Chat.ExpectedKind, Status: status.ModerationSupportStatus()}
	}
	if err := s.addPage(scope, request, &result); err != nil {
		return moderationFailure(request, pluginv1.ErrorCursorInvalid, "moderation cursor could not be created", false)
	}
	if validateErr := result.Validate(); validateErr != nil {
		return moderationFailure(request, pluginv1.ErrorConversion, validateErr.Error(), false)
	}
	data, _ := json.Marshal(result)
	if len(data) > int(pluginv1.DefaultMaxFrameSize) {
		return moderationFailure(request, pluginv1.ErrorResponseTruncated, "moderation result exceeds frame budget", false)
	}
	if key != "" {
		s.remember(key, result)
	}
	return result, nil
}

func memberStateToken(member pluginv1.ModerationMember) string {
	copy := member
	copy.StateToken = ""
	data, _ := json.Marshal(copy)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("state-%x", digest[:16])
}

func (s *ModerationService) position(scope ModerationScope, request *pluginv1.ModerationRequest) error {
	if request.Page == nil || request.Page.Cursor == "" {
		return nil
	}
	position, page, err := s.cursor.DecodeState(CursorScope{PluginID: scope.PluginID, AccountID: scope.AccountID, Operation: string(request.Operation), PeerID: request.Chat.ChatID, Query: request.Page.Filter + "\x00" + request.Page.Query}, request.Page.Cursor)
	if err != nil || position > int64(^uint(0)>>1) || page >= pluginv1.MaxClientPages {
		return ErrCursorInvalid
	}
	request.Page.Offset, request.Page.Page = int(position), page
	return nil
}

func (s *ModerationService) addPage(scope ModerationScope, request pluginv1.ModerationRequest, result *pluginv1.ModerationResult) error {
	if request.Page == nil || result.Page == nil || !result.Page.HasMore {
		return nil
	}
	next := request.Page.Offset
	switch {
	case len(result.Members) != 0:
		next += len(result.Members)
	case len(result.JoinRequests) != 0:
		next += len(result.JoinRequests)
	case len(result.Invites) != 0:
		next += len(result.Invites)
	default:
		return ErrCursorInvalid
	}
	cursor, err := s.cursor.EncodePage(CursorScope{PluginID: scope.PluginID, AccountID: scope.AccountID, Operation: string(request.Operation), PeerID: request.Chat.ChatID, Query: request.Page.Filter + "\x00" + request.Page.Query}, int64(next), request.Page.Page+1)
	if err != nil {
		return err
	}
	result.Page.Cursor = cursor
	return nil
}

func moderationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if _, ok := parent.Deadline(); ok {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(pluginv1.MaxModerationRequestSeconds)*time.Second)
}

func moderationReplayKey(scope ModerationScope, request pluginv1.ModerationRequest) string {
	if !request.Operation.Mutating() || request.IdempotencyKey == "" {
		return ""
	}
	target := "none"
	if request.Participant != nil {
		target = fmt.Sprintf("user:%d", request.Participant.UserID)
		if request.Participant.Username != "" {
			digest := sha256.Sum256([]byte(strings.ToLower(request.Participant.Username)))
			target = fmt.Sprintf("username:%x", digest[:8])
		}
		if request.Participant.Reply != nil {
			target = fmt.Sprintf("reply:%d", request.Participant.Reply.MessageID)
		}
	}
	return fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s\x00%s", scope.PluginID, scope.AccountID, request.Operation, request.Chat.ChatID, target, request.IdempotencyKey)
}

func (s *ModerationService) replay(key string) (pluginv1.ModerationResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		s.counters.idempotencyMiss.Add(1)
		return pluginv1.ModerationResult{}, false
	}
	entry := element.Value.(*moderationReplay)
	if s.clock().Sub(entry.at) > moderationResultRetention {
		delete(s.entries, key)
		s.lru.Remove(element)
		s.counters.idempotencyMiss.Add(1)
		s.counters.idempotencyEvict.Add(1)
		return pluginv1.ModerationResult{}, false
	}
	s.lru.MoveToFront(element)
	s.counters.idempotencyHit.Add(1)
	return entry.result, true
}

func (s *ModerationService) remember(key string, result pluginv1.ModerationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if old := s.entries[key]; old != nil {
		old.Value.(*moderationReplay).result = result
		old.Value.(*moderationReplay).at = s.clock()
		s.lru.MoveToFront(old)
		return
	}
	element := s.lru.PushFront(&moderationReplay{key: key, at: s.clock(), result: result})
	s.entries[key] = element
	for s.lru.Len() > s.maxEntries {
		old := s.lru.Back()
		delete(s.entries, old.Value.(*moderationReplay).key)
		s.lru.Remove(old)
		s.counters.idempotencyEvict.Add(1)
	}
}

func moderationFailure(request pluginv1.ModerationRequest, code pluginv1.ErrorCode, message string, retryable bool) (pluginv1.ModerationResult, error) {
	err := &pluginv1.Error{Code: code, Operation: string(request.Operation), Message: message, Retryable: retryable}
	result := moderationOutcome(request, pluginv1.ActionResultFailure)
	result.Error = err
	return result, err
}

func moderationOutcome(request pluginv1.ModerationRequest, status pluginv1.ActionResultState) pluginv1.ModerationResult {
	result := pluginv1.ModerationResult{Operation: request.Operation, RequestID: request.RequestID, ChatID: request.Chat.ChatID, Status: status}
	if request.Participant != nil {
		result.TargetUserID = request.Participant.UserID
	}
	return result
}

func moderationContextResult(request pluginv1.ModerationRequest, err error) (pluginv1.ModerationResult, error) {
	if request.Operation.Mutating() {
		wire := &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Operation: string(request.Operation), Message: "moderation mutation outcome is unknown after cancellation or timeout"}
		result := moderationOutcome(request, pluginv1.ActionResultOutcomeUnknown)
		result.Error = wire
		return result, wire
	}
	if errors.Is(err, context.Canceled) {
		return moderationFailure(request, pluginv1.ErrorDisconnected, "moderation request canceled", true)
	}
	return moderationFailure(request, pluginv1.ErrorDeadlineExceeded, "moderation request deadline exceeded", true)
}

func moderationPreflightContextResult(request pluginv1.ModerationRequest, err error) (pluginv1.ModerationResult, error) {
	if errors.Is(err, context.Canceled) {
		return moderationFailure(request, pluginv1.ErrorDisconnected, "moderation request canceled before invocation", true)
	}
	return moderationFailure(request, pluginv1.ErrorDeadlineExceeded, "moderation request deadline exceeded before invocation", true)
}

func (s *ModerationService) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	clear(s.entries)
	s.lru.Init()
	s.mu.Unlock()
	s.cancel()
	_ = s.rate.Close(context.Background())
}

func (s *ModerationService) Stats() ModerationStats {
	return ModerationStats{
		Requests: s.counters.requests.Load(), Success: s.counters.success.Load(), Failure: s.counters.failure.Load(),
		Noop: s.counters.noop.Load(), Partial: s.counters.partial.Load(), OutcomeUnknown: s.counters.outcomeUnknown.Load(),
		Unsupported: s.counters.unsupported.Load(), PermissionDenied: s.counters.permissionDenied.Load(), RateLimited: s.counters.rateLimited.Load(),
		IdempotencyHit: s.counters.idempotencyHit.Load(), IdempotencyMiss: s.counters.idempotencyMiss.Load(), IdempotencyEviction: s.counters.idempotencyEvict.Load(),
		LatencyMillis: s.counters.latencyMillis.Load(), Active: s.counters.active.Load(), MaxActive: s.counters.maxActive.Load(),
		OrderingEntries: s.ordering.len(), FairnessEntries: s.fairness.len(), Rate: s.rate.Stats(),
	}
}

func mapModerationError(request pluginv1.ModerationRequest, err error) (pluginv1.ModerationResult, error) {
	if errors.Is(err, errModerationNoChange) {
		result := moderationOutcome(request, pluginv1.ActionResultNoop)
		result.Acknowledged = true
		return result, nil
	}
	var wire *pluginv1.Error
	if errors.As(err, &wire) {
		result, _ := moderationFailure(request, wire.Code, wire.Message, wire.Retryable)
		result.Error = wire
		if wire.Code == pluginv1.ErrorOutcomeUnknown {
			result.Status = pluginv1.ActionResultOutcomeUnknown
		}
		return result, wire
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return moderationContextResult(request, err)
	}
	return moderationFailure(request, pluginv1.ErrorInternal, "Telegram moderation operation failed", false)
}
