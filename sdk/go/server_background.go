package plugin

import (
	"context"
	"errors"
	"sync"
	"time"
)

// backgroundStopTimeout bounds how long Stop waits for a BackgroundHandler
// that ignores cancellation; the lifecycle must not hang on plugin code.
const backgroundStopTimeout = 5 * time.Second

type backgroundRunner struct {
	parent     context.Context
	closeConn  context.CancelFunc
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	stopOnce   sync.Once
	stopSignal chan struct{}
}

func newBackgroundRunner(parent context.Context, closeConn context.CancelFunc) *backgroundRunner {
	return &backgroundRunner{parent: parent, closeConn: closeConn, stopSignal: make(chan struct{})}
}

func (b *backgroundRunner) start(handler BackgroundHandler, services Services) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(WithServices(b.parent, services))
	b.cancel, b.done = cancel, make(chan struct{})
	go func() {
		defer close(b.done)
		err := invokeValue(ctx, handler.Background)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		select {
		case <-b.stopSignal:
			// Errors raised while the SDK is stopping the handler are expected.
			return
		default:
		}
		b.mu.Lock()
		b.err = err
		b.mu.Unlock()
		b.closeConn()
	}()
}

// stop cancels the handler and waits for it within a bounded time. It is safe
// to call more than once and before start.
func (b *backgroundRunner) stop() {
	b.stopOnce.Do(func() { close(b.stopSignal) })
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	timer := time.NewTimer(backgroundStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (b *backgroundRunner) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}
