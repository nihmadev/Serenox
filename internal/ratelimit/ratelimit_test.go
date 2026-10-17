package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTokenBucketRefill(t *testing.T) {
	t.Parallel()
	limiter, err := New(Config{TokensPerSecond: 1, Burst: 2, MaxIdentities: 10, IdleTTL: time.Minute, SweepInterval: time.Second, Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if d := limiter.allowAt(base, "user", 1); !d.Allowed || d.Remaining != 1 {
		t.Fatalf("first = %#v", d)
	}
	if d := limiter.allowAt(base, "user", 1); !d.Allowed || d.Remaining != 0 {
		t.Fatalf("second = %#v", d)
	}
	if d := limiter.allowAt(base, "user", 1); d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("limited = %#v", d)
	}
	if d := limiter.allowAt(base.Add(500*time.Millisecond), "user", 1); d.Allowed || d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("half refill = %#v", d)
	}
	if d := limiter.allowAt(base.Add(time.Second), "user", 1); !d.Allowed {
		t.Fatalf("refilled = %#v", d)
	}
}

func TestIdentityCapacityAndIdleEviction(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{TokensPerSecond: 1, Burst: 1, MaxIdentities: 2, IdleTTL: time.Second, SweepInterval: 100 * time.Millisecond, Shards: 1})
	base := time.Now()
	if !limiter.allowAt(base, "a", 1).Allowed || !limiter.allowAt(base, "b", 1).Allowed {
		t.Fatal("initial identities rejected")
	}
	decision := limiter.allowAt(base, "c", 1)
	if decision.Allowed || !decision.CapacityLimited {
		t.Fatalf("capacity decision = %#v", decision)
	}
	if stats := limiter.Stats(); stats.Identities != 2 || stats.CapacityLimited != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if decision := limiter.allowAt(base.Add(time.Second), "c", 1); !decision.Allowed {
		t.Fatalf("idle eviction decision = %#v", decision)
	}
	if stats := limiter.Stats(); stats.Identities != 2 || stats.Evicted != 1 {
		t.Fatalf("post-eviction stats = %#v", stats)
	}
}

func TestSweepRemovesExpiredOnly(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{TokensPerSecond: 1, Burst: 1, MaxIdentities: 4, IdleTTL: time.Second, SweepInterval: time.Second, Shards: 2})
	base := time.Now()
	limiter.allowAt(base, "old", 1)
	limiter.allowAt(base.Add(750*time.Millisecond), "new", 1)
	if removed := limiter.Sweep(base.Add(time.Second)); removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if stats := limiter.Stats(); stats.Identities != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestInvalidInputDoesNotAllocateIdentity(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{MaxIdentities: 2, Shards: 1})
	for _, test := range []struct {
		key  string
		cost float64
	}{{"", 1}, {string(make([]byte, 257)), 1}, {"ok", 0}, {"ok", 1000}} {
		if limiter.allowAt(time.Now(), test.key, test.cost).Allowed {
			t.Fatalf("invalid input accepted: %#v", test)
		}
	}
	if got := limiter.Stats().Identities; got != 0 {
		t.Fatalf("invalid inputs allocated %d identities", got)
	}
}

func TestSingleSweeperLifecycle(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{IdleTTL: 20 * time.Millisecond, SweepInterval: 5 * time.Millisecond, MaxIdentities: 2, Shards: 1})
	parent, cancelParent := context.WithCancel(context.Background())
	if err := limiter.Start(parent); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Start(parent); err != ErrAlreadyStarted {
		t.Fatalf("second Start error = %v", err)
	}
	limiter.Allow("user")
	time.Sleep(30 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for limiter.Stats().Identities != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if limiter.Stats().Identities != 0 {
		t.Fatal("sweeper did not evict idle identity")
	}
	cancelParent()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := limiter.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWithoutStart(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := limiter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Start(context.Background()); err != ErrAlreadyStarted {
		t.Fatalf("Start after close error = %v", err)
	}
}

func TestConcurrentCardinalityBound(t *testing.T) {
	limiter, _ := New(Config{TokensPerSecond: 100, Burst: 10, MaxIdentities: 128, IdleTTL: time.Hour, SweepInterval: time.Minute, Shards: 8})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				limiter.Allow(fmt.Sprintf("%d-%d", worker, j))
			}
		}(i)
	}
	wg.Wait()
	stats := limiter.Stats()
	if stats.Identities > stats.MaxIdentities {
		t.Fatalf("identity bound exceeded: %#v", stats)
	}
	if stats.CapacityLimited == 0 {
		t.Fatalf("overload did not reject identities: %#v", stats)
	}
}

func TestShardsPartitionExactCapacity(t *testing.T) {
	t.Parallel()
	limiter, _ := New(Config{MaxIdentities: 10, Shards: 3})
	total := 0
	for i := range limiter.shards {
		total += limiter.shards[i].capacity
	}
	if total != 10 {
		t.Fatalf("shard capacity total = %d", total)
	}
}

func BenchmarkAllowExistingIdentity(b *testing.B) {
	limiter, _ := New(Config{TokensPerSecond: 1e9, Burst: 1e9, MaxIdentities: 10_000, Shards: 16})
	limiter.Allow("user")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			limiter.Allow("user")
		}
	})
}
