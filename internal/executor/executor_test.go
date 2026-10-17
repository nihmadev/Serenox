package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func job(id string) Job { return Job{ID: id, HandlerID: "handler", Kind: KindWatcher} }

func TestOverloadIsBounded(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	pool, err := NewPool("plugin", Config{QueueCapacity: 2, Workers: 1, HandlerTimeout: time.Second}, func(ctx context.Context, _ Job) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pool.TrySubmit(job("running")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := pool.TrySubmit(job("queued-1")); err != nil {
		t.Fatal(err)
	}
	if err := pool.TrySubmit(job("queued-2")); err != nil {
		t.Fatal(err)
	}
	if err := pool.TrySubmit(job("rejected")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overload error = %v", err)
	}
	stats := pool.Stats()
	if stats.QueueDepth != 2 || stats.Accepted != 3 || stats.Rejected != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := pool.Stats(); stats.Completed != 3 || stats.QueueDepth != 0 {
		t.Fatalf("final stats = %#v", stats)
	}
}

func TestFixedWorkerConcurrency(t *testing.T) {
	t.Parallel()
	var active, maximum atomic.Int64
	pool, _ := NewPool("plugin", Config{QueueCapacity: 32, Workers: 3, HandlerTimeout: time.Second}, func(context.Context, Job) error {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		active.Add(-1)
		return nil
	}, Hooks{})
	_ = pool.Start(context.Background())
	for i := 0; i < 30; i++ {
		if err := pool.TrySubmit(job(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got > 3 || got < 2 {
		t.Fatalf("maximum concurrency = %d", got)
	}
}

func TestTimeoutAndPanicRecovery(t *testing.T) {
	t.Parallel()
	results := make(chan Result, 3)
	pool, _ := NewPool("plugin", Config{QueueCapacity: 3, Workers: 1, HandlerTimeout: 20 * time.Millisecond}, func(ctx context.Context, value Job) error {
		switch value.ID {
		case "panic":
			panic("boom")
		case "timeout":
			<-ctx.Done()
			return nil
		default:
			return nil
		}
	}, Hooks{OnResult: func(result Result) { results <- result }})
	_ = pool.Start(context.Background())
	for _, id := range []string{"panic", "timeout", "after"} {
		if err := pool.TrySubmit(job(id)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]Result)
	for i := 0; i < 3; i++ {
		result := <-results
		seen[result.Job.ID] = result
	}
	if !seen["panic"].Panicked {
		t.Fatalf("panic result = %#v", seen["panic"])
	}
	var panicErr *PanicError
	if !errors.As(seen["panic"].Error, &panicErr) || len(panicErr.Stack) == 0 {
		t.Fatalf("panic error = %v", seen["panic"].Error)
	}
	if !seen["timeout"].TimedOut || !errors.Is(seen["timeout"].Error, ErrHandlerTimeout) {
		t.Fatalf("timeout result = %#v", seen["timeout"])
	}
	if seen["after"].Error != nil {
		t.Fatalf("post-panic job failed: %v", seen["after"].Error)
	}
	stats := pool.Stats()
	if stats.Panics != 1 || stats.Timeouts != 1 || stats.Completed != 3 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestCloseDrainsAndRejectsAdmission(t *testing.T) {
	t.Parallel()
	var handled atomic.Int32
	pool, _ := NewPool("plugin", Config{QueueCapacity: 16, Workers: 2, HandlerTimeout: time.Second}, func(context.Context, Job) error { handled.Add(1); return nil }, Hooks{})
	_ = pool.Start(context.Background())
	for i := 0; i < 16; i++ {
		if err := pool.TrySubmit(job(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if handled.Load() != 16 {
		t.Fatalf("handled = %d", handled.Load())
	}
	if err := pool.TrySubmit(job("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("late error = %v", err)
	}
}

func TestCloseUnblocksWaitingSubmitAndCancelsHandler(t *testing.T) {
	started := make(chan struct{})
	pool, _ := NewPool("plugin", Config{QueueCapacity: 1, Workers: 1, HandlerTimeout: time.Minute}, func(ctx context.Context, _ Job) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}, Hooks{})
	_ = pool.Start(context.Background())
	_ = pool.TrySubmit(job("running"))
	<-started
	_ = pool.TrySubmit(job("queued"))
	submitDone := make(chan error, 1)
	go func() { submitDone <- pool.Submit(context.Background(), job("waiting")) }()
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := pool.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v", err)
	}
	select {
	case err := <-submitDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Submit error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit remained blocked")
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	if err := pool.Close(secondCtx); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidJobsAndLifecycle(t *testing.T) {
	t.Parallel()
	pool, _ := NewPool("p", Config{}, func(context.Context, Job) error { return nil }, Hooks{})
	if err := pool.TrySubmit(job("x")); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("pre-start error = %v", err)
	}
	_ = pool.Start(context.Background())
	if err := pool.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second start error = %v", err)
	}
	if err := pool.TrySubmit(Job{Kind: "invalid", HandlerID: "h"}); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("invalid job error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = pool.Close(ctx)
}

func TestManagerBoundsAndShutdown(t *testing.T) {
	t.Parallel()
	manager, err := NewManager(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	handler := func(context.Context, Job) error { return nil }
	if _, err := manager.Register("one", Config{}, handler, Hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register("one", Config{}, handler, Hooks{}); !errors.Is(err, ErrPluginExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := manager.Register("two", Config{}, handler, Hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register("three", Config{}, handler, Hooks{}); !errors.Is(err, ErrPluginLimit) {
		t.Fatalf("limit error = %v", err)
	}
	if err := manager.TrySubmit("missing", job("x")); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Unregister(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register("three", Config{}, handler, Hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSubmitAndClose(t *testing.T) {
	pool, _ := NewPool("plugin", Config{QueueCapacity: 64, Workers: 4, HandlerTimeout: time.Second}, func(context.Context, Job) error { return nil }, Hooks{})
	_ = pool.Start(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				err := pool.TrySubmit(job(fmt.Sprintf("%d-%d", id, j)))
				if errors.Is(err, ErrClosed) {
					return
				}
			}
		}(i)
	}
	time.Sleep(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func BenchmarkPoolDispatch(b *testing.B) {
	pool, _ := NewPool("benchmark", Config{QueueCapacity: 1024, Workers: 4, HandlerTimeout: time.Second}, func(context.Context, Job) error { return nil }, Hooks{})
	_ = pool.Start(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := pool.Submit(ctx, job("benchmark"))
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		b.Fatal(err)
	}
}
