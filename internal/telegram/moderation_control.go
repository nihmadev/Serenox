package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nihmadev/Serenox/internal/ratelimit"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	moderationOrderingEntries = 512
	moderationOrderingWaiters = 4
	moderationFairnessEntries = 4096
	moderationPerScopeActive  = 2
)

var errModerationNoChange = fmt.Errorf("moderation state is already applied")

type moderationOrderingEntry struct {
	token chan struct{}
	refs  int
}

type moderationFairness struct {
	mu     sync.Mutex
	active map[string]int
}

func newModerationFairness() *moderationFairness {
	return &moderationFairness{active: make(map[string]int, moderationFairnessEntries)}
}

func (f *moderationFairness) acquire(scope ModerationScope) (func(), error) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", scope.PluginID, scope.AccountID)))
	key := hex.EncodeToString(digest[:16])
	f.mu.Lock()
	if f.active[key] >= moderationPerScopeActive || f.active[key] == 0 && len(f.active) >= moderationFairnessEntries {
		f.mu.Unlock()
		return nil, &pluginv1.Error{Code: pluginv1.ErrorOverloaded, Message: "moderation per-plugin concurrency limit reached", Retryable: true}
	}
	f.active[key]++
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.active[key]--
			if f.active[key] == 0 {
				delete(f.active, key)
			}
			f.mu.Unlock()
		})
	}, nil
}

func (f *moderationFairness) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.active)
}

// moderationOrdering is a bounded, lifecycle-cancelable keyed mutex. Entries
// exist only while a holder or waiter is active; it owns no goroutine or timer.
type moderationOrdering struct {
	mu      sync.Mutex
	entries map[string]*moderationOrderingEntry
}

func newModerationOrdering() *moderationOrdering {
	return &moderationOrdering{entries: make(map[string]*moderationOrderingEntry, moderationOrderingEntries)}
}

func (o *moderationOrdering) acquire(ctx context.Context, key string) (func(), error) {
	o.mu.Lock()
	entry := o.entries[key]
	if entry == nil {
		if len(o.entries) >= moderationOrderingEntries {
			o.mu.Unlock()
			return nil, &pluginv1.Error{Code: pluginv1.ErrorOverloaded, Message: "moderation ordering capacity reached", Retryable: true}
		}
		entry = &moderationOrderingEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		o.entries[key] = entry
	}
	if entry.refs >= moderationOrderingWaiters {
		o.mu.Unlock()
		return nil, &pluginv1.Error{Code: pluginv1.ErrorOverloaded, Message: "moderation target has too many waiters", Retryable: true}
	}
	entry.refs++
	o.mu.Unlock()

	select {
	case <-entry.token:
		var once sync.Once
		return func() { once.Do(func() { o.release(key, entry, true) }) }, nil
	case <-ctx.Done():
		o.release(key, entry, false)
		return nil, ctx.Err()
	}
}

func (o *moderationOrdering) release(key string, entry *moderationOrderingEntry, held bool) {
	if held {
		entry.token <- struct{}{}
	}
	o.mu.Lock()
	entry.refs--
	if entry.refs == 0 && o.entries[key] == entry {
		delete(o.entries, key)
	}
	o.mu.Unlock()
}

func (o *moderationOrdering) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.entries)
}

type moderationCounters struct {
	requests         atomic.Uint64
	success          atomic.Uint64
	failure          atomic.Uint64
	noop             atomic.Uint64
	partial          atomic.Uint64
	outcomeUnknown   atomic.Uint64
	unsupported      atomic.Uint64
	permissionDenied atomic.Uint64
	rateLimited      atomic.Uint64
	idempotencyHit   atomic.Uint64
	idempotencyMiss  atomic.Uint64
	idempotencyEvict atomic.Uint64
	latencyMillis    atomic.Uint64
	active           atomic.Int64
	maxActive        atomic.Int64
}

// ModerationStats is an aggregate, privacy-safe snapshot. It deliberately has
// no plugin text, IDs, usernames, reasons, ranks, invite links, or keys.
type ModerationStats struct {
	Requests            uint64
	Success             uint64
	Failure             uint64
	Noop                uint64
	Partial             uint64
	OutcomeUnknown      uint64
	Unsupported         uint64
	PermissionDenied    uint64
	RateLimited         uint64
	IdempotencyHit      uint64
	IdempotencyMiss     uint64
	IdempotencyEviction uint64
	LatencyMillis       uint64
	Active              int64
	MaxActive           int64
	OrderingEntries     int
	FairnessEntries     int
	Rate                ratelimit.Stats
}

func newModerationRateLimiter() (*ratelimit.Limiter, error) {
	return ratelimit.New(ratelimit.Config{
		TokensPerSecond: 4,
		Burst:           20,
		MaxIdentities:   8192,
		IdleTTL:         10 * time.Minute,
		SweepInterval:   time.Minute,
		Shards:          16,
	})
}

func moderationRateIdentity(scope ModerationScope, request pluginv1.ModerationRequest) string {
	value := fmt.Sprintf("%s\x00%d\x00%s\x00%d", scope.PluginID, scope.AccountID, moderationOperationGroup(request.Operation), request.Chat.ChatID)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func moderationOperationGroup(operation pluginv1.ModerationOperation) string {
	value := string(operation)
	for index := range value {
		if value[index] == '.' {
			return value[:index]
		}
	}
	return "unknown"
}

func moderationRateCost(request pluginv1.ModerationRequest) float64 {
	items := len(request.Messages)
	if items < 1 {
		items = 1
	}
	return 1 + float64(items-1)/25
}

func moderationOrderingKey(scope ModerationScope, request pluginv1.ModerationRequest) string {
	if !request.Operation.Mutating() {
		return ""
	}
	target := moderationOperationGroup(request.Operation)
	if request.Participant != nil {
		target = moderationParticipantIdentity(*request.Participant)
	} else if request.Topic != nil {
		target = fmt.Sprintf("topic:%d", request.Topic.TopicID)
	} else if len(request.Messages) == 1 {
		target = fmt.Sprintf("message:%d", request.Messages[0].MessageID)
	}
	return fmt.Sprintf("%d\x00%d\x00%s", scope.AccountID, request.Chat.ChatID, target)
}

func moderationParticipantIdentity(target pluginv1.ModerationParticipantTarget) string {
	if target.UserID != 0 {
		return fmt.Sprintf("user:%d", target.UserID)
	}
	if target.Reply != nil {
		return fmt.Sprintf("reply:%d", target.Reply.MessageID)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(target.Username)))
	return "username:" + hex.EncodeToString(digest[:8])
}

func (c *moderationCounters) begin() func(pluginv1.ModerationResult, error) {
	c.requests.Add(1)
	active := c.active.Add(1)
	for {
		previous := c.maxActive.Load()
		if active <= previous || c.maxActive.CompareAndSwap(previous, active) {
			break
		}
	}
	started := time.Now()
	return func(result pluginv1.ModerationResult, err error) {
		c.active.Add(-1)
		c.latencyMillis.Add(uint64(time.Since(started).Milliseconds()))
		switch result.Status {
		case pluginv1.ActionResultSuccess:
			c.success.Add(1)
		case pluginv1.ActionResultNoop, pluginv1.ActionResultSkipped:
			c.noop.Add(1)
		case pluginv1.ActionResultPartial:
			c.partial.Add(1)
		case pluginv1.ActionResultOutcomeUnknown:
			c.outcomeUnknown.Add(1)
		default:
			c.failure.Add(1)
		}
		var wire *pluginv1.Error
		if err != nil && errors.As(err, &wire) {
			switch wire.Code {
			case pluginv1.ErrorUnsupported, pluginv1.ErrorModerationUnsupportedChat:
				c.unsupported.Add(1)
			case pluginv1.ErrorPermissionDenied:
				c.permissionDenied.Add(1)
			case pluginv1.ErrorRateLimited:
				c.rateLimited.Add(1)
			}
		}
	}
}
