// Package executor runs plugin work in fixed worker pools with bounded queues.
package executor

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrQueueFull      = errors.New("executor: plugin queue is full")
	ErrClosed         = errors.New("executor: pool is closed")
	ErrNotStarted     = errors.New("executor: pool is not started")
	ErrAlreadyStarted = errors.New("executor: pool is already started")
	ErrInvalidJob     = errors.New("executor: invalid job")
	ErrHandlerTimeout = errors.New("executor: handler deadline exceeded")
	ErrPluginExists   = errors.New("executor: plugin already registered")
	ErrPluginNotFound = errors.New("executor: plugin is not registered")
	ErrPluginLimit    = errors.New("executor: plugin limit reached")
)

type Kind string

const (
	KindCommand   Kind = "command"
	KindWatcher   Kind = "watcher"
	KindInline    Kind = "inline"
	KindCallback  Kind = "callback"
	KindDeleted   Kind = "deleted_message"
	KindLifecycle Kind = "lifecycle"
	KindScheduled Kind = "scheduled"
	KindEvent     Kind = "event"
)

// Job is an immutable work envelope. Payload bounds are enforced before this layer.
type Job struct {
	ID        string
	HandlerID string
	Kind      Kind
	Payload   any
	Enqueued  time.Time
	Timeout   time.Duration
}

type Handler func(context.Context, Job) error

// Config is validated into hard per-plugin resource limits.
type Config struct {
	QueueCapacity  int
	Workers        int
	HandlerTimeout time.Duration
}

func (c Config) normalized() (Config, error) {
	if c.QueueCapacity == 0 {
		c.QueueCapacity = 128
	}
	if c.Workers == 0 {
		c.Workers = 2
	}
	if c.HandlerTimeout == 0 {
		c.HandlerTimeout = 15 * time.Second
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 65536 {
		return Config{}, fmt.Errorf("executor: queue capacity must be between 1 and 65536")
	}
	if c.Workers < 1 || c.Workers > 256 {
		return Config{}, fmt.Errorf("executor: workers must be between 1 and 256")
	}
	if c.HandlerTimeout < time.Millisecond || c.HandlerTimeout > 24*time.Hour {
		return Config{}, fmt.Errorf("executor: handler timeout must be between 1ms and 24h")
	}
	return c, nil
}

// Validate reports whether the worker and queue bounds are usable.
func (c Config) Validate() error {
	_, err := c.normalized()
	return err
}

type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("executor: handler panic: %v", e.Value) }

type Result struct {
	PluginID string
	Job      Job
	Started  time.Time
	Finished time.Time
	Error    error
	Panicked bool
	TimedOut bool
}

// Hooks must return promptly. Panics in hooks are recovered by the pool.
type Hooks struct {
	OnResult func(Result)
	OnReject func(Job, error)
}

type Stats struct {
	QueueDepth    int
	QueueCapacity int
	Workers       int
	Accepted      uint64
	Completed     uint64
	Rejected      uint64
	Failed        uint64
	Panics        uint64
	Timeouts      uint64
	Active        int64
}

// Pool owns one plugin's queue, workers, cancellation, and result accounting.
type Pool struct {
	pluginID      string
	config        Config
	handler       Handler
	hooks         Hooks
	queue         chan Job
	admissionStop chan struct{}
	admissionOnce sync.Once
	startMu       sync.Mutex
	submitMu      sync.RWMutex
	state         atomic.Int32 // 0 new, 1 running, 2 stopping/stopped
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	done          chan struct{}
	accepted      atomic.Uint64
	completed     atomic.Uint64
	rejected      atomic.Uint64
	failed        atomic.Uint64
	panics        atomic.Uint64
	timeouts      atomic.Uint64
	active        atomic.Int64
}

func NewPool(pluginID string, config Config, handler Handler, hooks Hooks) (*Pool, error) {
	if pluginID == "" || len(pluginID) > 256 {
		return nil, fmt.Errorf("executor: invalid plugin ID")
	}
	if handler == nil {
		return nil, fmt.Errorf("executor: nil handler")
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Pool{pluginID: pluginID, config: config, handler: handler, hooks: hooks, queue: make(chan Job, config.QueueCapacity), admissionStop: make(chan struct{}), done: make(chan struct{})}, nil
}

// Start creates exactly Config.Workers workers and one owned completion waiter.
func (p *Pool) Start(parent context.Context) error {
	if parent == nil {
		return fmt.Errorf("executor: nil parent context")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.state.Load() != 0 {
		return ErrAlreadyStarted
	}
	p.ctx, p.cancel = context.WithCancel(parent)
	p.state.Store(1)
	p.wg.Add(p.config.Workers)
	for i := 0; i < p.config.Workers; i++ {
		go p.worker()
	}
	go func() { p.wg.Wait(); close(p.done) }()
	return nil
}

func validateJob(job Job) error {
	if job.HandlerID == "" || len(job.HandlerID) > 256 || len(job.ID) > 256 {
		return ErrInvalidJob
	}
	switch job.Kind {
	case KindCommand, KindWatcher, KindInline, KindCallback, KindDeleted, KindLifecycle, KindScheduled, KindEvent:
	default:
		return ErrInvalidJob
	}
	return nil
}

// TrySubmit provides non-blocking backpressure, normally used for watchers.
func (p *Pool) TrySubmit(job Job) error {
	if err := validateJob(job); err != nil {
		return err
	}
	if p.state.Load() == 0 {
		return ErrNotStarted
	}
	if p.state.Load() != 1 {
		return ErrClosed
	}
	if job.Enqueued.IsZero() {
		job.Enqueued = time.Now()
	}
	p.submitMu.RLock()
	if p.ctx.Err() != nil {
		p.submitMu.RUnlock()
		p.reject(job, ErrClosed)
		return ErrClosed
	}
	var admissionErr error
	select {
	case <-p.admissionStop:
		admissionErr = ErrClosed
	case p.queue <- job:
		p.accepted.Add(1)
		p.submitMu.RUnlock()
		return nil
	default:
		admissionErr = ErrQueueFull
	}
	p.submitMu.RUnlock()
	p.reject(job, admissionErr)
	return admissionErr
}

// Submit waits only until ctx is canceled, providing bounded command backpressure.
func (p *Pool) Submit(ctx context.Context, job Job) error {
	if err := validateJob(job); err != nil {
		return err
	}
	if p.state.Load() == 0 {
		return ErrNotStarted
	}
	if p.state.Load() != 1 {
		return ErrClosed
	}
	if job.Enqueued.IsZero() {
		job.Enqueued = time.Now()
	}
	p.submitMu.RLock()
	var admissionErr error
	select {
	case <-p.admissionStop:
		admissionErr = ErrClosed
	case <-p.ctx.Done():
		admissionErr = ErrClosed
	case <-ctx.Done():
		admissionErr = ctx.Err()
	case p.queue <- job:
		p.accepted.Add(1)
		p.submitMu.RUnlock()
		return nil
	}
	p.submitMu.RUnlock()
	p.reject(job, admissionErr)
	return admissionErr
}

func (p *Pool) reject(job Job, err error) { p.rejected.Add(1); safeReject(p.hooks.OnReject, job, err) }

func safeReject(hook func(Job, error), job Job, err error) {
	if hook == nil {
		return
	}
	defer func() { _ = recover() }()
	hook(job, err)
}
func safeResult(hook func(Result), result Result) {
	if hook == nil {
		return
	}
	defer func() { _ = recover() }()
	hook(result)
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.queue:
			if !ok {
				return
			}
			p.execute(job)
		}
	}
}

func (p *Pool) execute(job Job) {
	result := Result{PluginID: p.pluginID, Job: job, Started: time.Now()}
	p.active.Add(1)
	timeout := p.config.HandlerTimeout
	if job.Timeout > 0 && job.Timeout < timeout {
		timeout = job.Timeout
	}
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	func() {
		defer func() {
			if v := recover(); v != nil {
				stack := debug.Stack()
				if len(stack) > 64*1024 {
					stack = stack[:64*1024]
				}
				result.Error = &PanicError{Value: v, Stack: stack}
				result.Panicked = true
			}
		}()
		result.Error = p.handler(ctx, job)
	}()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		if result.Error == nil {
			result.Error = ErrHandlerTimeout
		}
	}
	cancel()
	result.Finished = time.Now()
	p.active.Add(-1)
	p.completed.Add(1)
	if result.Error != nil {
		p.failed.Add(1)
	}
	if result.Panicked {
		p.panics.Add(1)
	}
	if result.TimedOut {
		p.timeouts.Add(1)
	}
	safeResult(p.hooks.OnResult, result)
}

// Close stops admission, drains accepted work, and cancels workers if ctx expires.
func (p *Pool) Close(ctx context.Context) error {
	state := p.state.Load()
	if state == 0 {
		return ErrNotStarted
	}
	if p.state.CompareAndSwap(1, 2) {
		p.admissionOnce.Do(func() { close(p.admissionStop) })
		p.submitMu.Lock()
		close(p.queue)
		p.submitMu.Unlock()
	}
	select {
	case <-p.done:
		p.cancel()
		return nil
	case <-ctx.Done():
		p.cancel()
		return ctx.Err()
	}
}

func (p *Pool) Stats() Stats {
	return Stats{QueueDepth: len(p.queue), QueueCapacity: cap(p.queue), Workers: p.config.Workers, Accepted: p.accepted.Load(), Completed: p.completed.Load(), Rejected: p.rejected.Load(), Failed: p.failed.Load(), Panics: p.panics.Load(), Timeouts: p.timeouts.Load(), Active: p.active.Load()}
}

// Manager bounds the number of independently queued plugins.
type Manager struct {
	mu     sync.RWMutex
	ctx    context.Context
	max    int
	closed bool
	pools  map[string]*Pool
}

func NewManager(ctx context.Context, maxPlugins int) (*Manager, error) {
	if ctx == nil {
		return nil, fmt.Errorf("executor: nil manager context")
	}
	if maxPlugins < 1 || maxPlugins > 4096 {
		return nil, fmt.Errorf("executor: max plugins must be between 1 and 4096")
	}
	return &Manager{ctx: ctx, max: maxPlugins, pools: make(map[string]*Pool)}, nil
}

func (m *Manager) Register(pluginID string, config Config, handler Handler, hooks Hooks) (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if _, ok := m.pools[pluginID]; ok {
		return nil, ErrPluginExists
	}
	if len(m.pools) >= m.max {
		return nil, ErrPluginLimit
	}
	pool, err := NewPool(pluginID, config, handler, hooks)
	if err != nil {
		return nil, err
	}
	if err := pool.Start(m.ctx); err != nil {
		return nil, err
	}
	m.pools[pluginID] = pool
	return pool, nil
}

func (m *Manager) Pool(pluginID string) (*Pool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pools[pluginID]
	return p, ok
}
func (m *Manager) TrySubmit(pluginID string, job Job) error {
	p, ok := m.Pool(pluginID)
	if !ok {
		return ErrPluginNotFound
	}
	return p.TrySubmit(job)
}
func (m *Manager) Submit(ctx context.Context, pluginID string, job Job) error {
	p, ok := m.Pool(pluginID)
	if !ok {
		return ErrPluginNotFound
	}
	return p.Submit(ctx, job)
}
func (m *Manager) Unregister(ctx context.Context, pluginID string) error {
	m.mu.Lock()
	p, ok := m.pools[pluginID]
	if ok {
		delete(m.pools, pluginID)
	}
	m.mu.Unlock()
	if !ok {
		return ErrPluginNotFound
	}
	return p.Close(ctx)
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	pools := make([]*Pool, 0, len(m.pools))
	for _, p := range m.pools {
		pools = append(pools, p)
	}
	clear(m.pools)
	m.mu.Unlock()
	var first error
	for _, p := range pools {
		if err := p.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
