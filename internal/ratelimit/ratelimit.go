// Package ratelimit implements a bounded sharded token bucket.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrAlreadyStarted = errors.New("ratelimit: sweeper already started")
	ErrInvalidKey     = errors.New("ratelimit: invalid identity")
	ErrInvalidCost    = errors.New("ratelimit: invalid cost")
)

type Config struct {
	TokensPerSecond float64
	Burst           float64
	MaxIdentities   int
	IdleTTL         time.Duration
	SweepInterval   time.Duration
	Shards          int
}

func (c Config) normalized() (Config, error) {
	if c.TokensPerSecond == 0 {
		c.TokensPerSecond = 2
	}
	if c.Burst == 0 {
		c.Burst = 30
	}
	if c.MaxIdentities == 0 {
		c.MaxIdentities = 10_000
	}
	if c.IdleTTL == 0 {
		c.IdleTTL = 10 * time.Minute
	}
	if c.SweepInterval == 0 {
		c.SweepInterval = time.Minute
	}
	if c.Shards == 0 {
		c.Shards = 16
	}
	if math.IsNaN(c.TokensPerSecond) || math.IsInf(c.TokensPerSecond, 0) || c.TokensPerSecond <= 0 || c.TokensPerSecond > 1e9 {
		return Config{}, fmt.Errorf("ratelimit: tokens per second must be in (0, 1e9]")
	}
	if math.IsNaN(c.Burst) || math.IsInf(c.Burst, 0) || c.Burst < 1 || c.Burst > 1e9 {
		return Config{}, fmt.Errorf("ratelimit: burst must be in [1, 1e9]")
	}
	if c.MaxIdentities < 1 || c.MaxIdentities > 10_000_000 {
		return Config{}, fmt.Errorf("ratelimit: max identities must be between 1 and 10000000")
	}
	if c.Shards < 1 || c.Shards > 1024 || c.Shards > c.MaxIdentities {
		return Config{}, fmt.Errorf("ratelimit: shards must be between 1 and max identities")
	}
	if c.IdleTTL < time.Millisecond || c.IdleTTL > 365*24*time.Hour {
		return Config{}, fmt.Errorf("ratelimit: idle TTL out of range")
	}
	if c.SweepInterval < time.Millisecond || c.SweepInterval > c.IdleTTL {
		return Config{}, fmt.Errorf("ratelimit: sweep interval must be in [1ms, idle TTL]")
	}
	return c, nil
}

// Validate reports whether rate, cardinality, and sweeper bounds are usable.
func (c Config) Validate() error {
	_, err := c.normalized()
	return err
}

type entry struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}
type shard struct {
	mu       sync.Mutex
	entries  map[string]entry
	capacity int
}

type Decision struct {
	Allowed         bool
	Remaining       float64
	RetryAfter      time.Duration
	CapacityLimited bool
}

type Stats struct {
	Identities      int
	MaxIdentities   int
	Allowed         uint64
	Limited         uint64
	CapacityLimited uint64
	Evicted         uint64
}

// Limiter holds at most Config.MaxIdentities and uses at most one ticker.
type Limiter struct {
	config          Config
	shards          []shard
	state           atomic.Int32 // 0 idle, 1 sweeper running, 2 closed
	lifecycleMu     sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	allowed         atomic.Uint64
	limited         atomic.Uint64
	capacityLimited atomic.Uint64
	evicted         atomic.Uint64
}

func New(config Config) (*Limiter, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	l := &Limiter{config: config, shards: make([]shard, config.Shards), done: make(chan struct{})}
	base, remainder := config.MaxIdentities/config.Shards, config.MaxIdentities%config.Shards
	for i := range l.shards {
		cap := base
		if i < remainder {
			cap++
		}
		l.shards[i] = shard{entries: make(map[string]entry, cap), capacity: cap}
	}
	return l, nil
}

func hashKey(key string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for index := 0; index < len(key); index++ {
		hash ^= uint64(key[index])
		hash *= prime
	}
	return hash
}

func (l *Limiter) shardFor(key string) *shard { return &l.shards[hashKey(key)%uint64(len(l.shards))] }

// Allow consumes one token at time.Now.
func (l *Limiter) Allow(identity string) Decision { return l.AllowN(identity, 1) }

// AllowN consumes cost tokens without creating a goroutine or timer.
func (l *Limiter) AllowN(identity string, cost float64) Decision {
	return l.allowAt(time.Now(), identity, cost)
}

func (l *Limiter) allowAt(now time.Time, identity string, cost float64) Decision {
	if identity == "" || len(identity) > 256 {
		l.limited.Add(1)
		return Decision{RetryAfter: l.config.SweepInterval}
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 || cost > l.config.Burst {
		l.limited.Add(1)
		return Decision{RetryAfter: l.config.SweepInterval}
	}
	s := l.shardFor(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, exists := s.entries[identity]
	if !exists {
		if len(s.entries) >= s.capacity {
			oldestKey := ""
			var oldest time.Time
			for key, candidate := range s.entries {
				if now.Sub(candidate.lastSeen) >= l.config.IdleTTL && (oldestKey == "" || candidate.lastSeen.Before(oldest)) {
					oldestKey, oldest = key, candidate.lastSeen
				}
			}
			if oldestKey == "" {
				l.limited.Add(1)
				l.capacityLimited.Add(1)
				return Decision{RetryAfter: l.config.SweepInterval, CapacityLimited: true}
			}
			delete(s.entries, oldestKey)
			l.evicted.Add(1)
		}
		e = entry{tokens: l.config.Burst, last: now, lastSeen: now}
	} else {
		elapsed := now.Sub(e.last).Seconds()
		if elapsed > 0 {
			e.tokens = math.Min(l.config.Burst, e.tokens+elapsed*l.config.TokensPerSecond)
			e.last = now
		}
		e.lastSeen = now
	}
	if e.tokens >= cost {
		e.tokens -= cost
		s.entries[identity] = e
		l.allowed.Add(1)
		return Decision{Allowed: true, Remaining: e.tokens}
	}
	missing := cost - e.tokens
	retry := time.Duration(math.Ceil(missing / l.config.TokensPerSecond * float64(time.Second)))
	s.entries[identity] = e
	l.limited.Add(1)
	return Decision{Remaining: e.tokens, RetryAfter: retry}
}

// Sweep removes all identities idle for at least IdleTTL.
func (l *Limiter) Sweep(now time.Time) int {
	removed := 0
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		for key, e := range s.entries {
			if now.Sub(e.lastSeen) >= l.config.IdleTTL {
				delete(s.entries, key)
				removed++
			}
		}
		s.mu.Unlock()
	}
	if removed > 0 {
		l.evicted.Add(uint64(removed))
	}
	return removed
}

// Start owns the limiter's only periodic ticker until parent cancellation or Close.
func (l *Limiter) Start(parent context.Context) error {
	if parent == nil {
		return fmt.Errorf("ratelimit: nil parent context")
	}
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()
	if l.state.Load() != 0 {
		return ErrAlreadyStarted
	}
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.state.Store(1)
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(l.config.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				l.Sweep(now)
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (l *Limiter) Close(ctx context.Context) error {
	l.lifecycleMu.Lock()
	state := l.state.Load()
	if state == 0 {
		l.state.Store(2)
		close(l.done)
		l.lifecycleMu.Unlock()
		return nil
	}
	if state == 1 {
		l.state.Store(2)
		l.cancel()
	}
	l.lifecycleMu.Unlock()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) Stats() Stats {
	identities := 0
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		identities += len(s.entries)
		s.mu.Unlock()
	}
	return Stats{Identities: identities, MaxIdentities: l.config.MaxIdentities, Allowed: l.allowed.Load(), Limited: l.limited.Load(), CapacityLimited: l.capacityLimited.Load(), Evicted: l.evicted.Load()}
}
