package jobs

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nihmadev/Serenox/internal/executor"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Executor interface {
	TrySubmit(string, executor.Job) error
}
type Run struct {
	Invocation protocol.JobInvocation
	Complete   func(error)
}
type Config struct {
	Maximum  int
	Clock    Clock
	Executor Executor
}
type item struct {
	key   string
	at    time.Time
	index int
}
type scheduleHeap []*item

func (h scheduleHeap) Len() int           { return len(h) }
func (h scheduleHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h scheduleHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *scheduleHeap) Push(value any) {
	entry := value.(*item)
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *scheduleHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	last.index = -1
	return last
}

type Scheduler struct {
	mu              sync.Mutex
	store           JobStore
	config          Config
	records         map[string]Record
	queue           scheduleHeap
	wake            chan struct{}
	done            chan struct{}
	stop            chan struct{}
	running         bool
	loopAlive       atomic.Bool
	storeFailures   uint64
	restoreFailures uint64
	interrupted     uint64
}

func New(store JobStore, config Config) (*Scheduler, error) {
	if store == nil {
		return nil, errors.New("jobs: store is required")
	}
	if config.Executor == nil {
		return nil, errors.New("jobs: executor is required")
	}
	if config.Maximum == 0 {
		config.Maximum = protocol.MaxJobsPerPlugin
	}
	if config.Maximum < 1 || config.Maximum > 65536 {
		return nil, errors.New("jobs: maximum is outside bounds")
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	return &Scheduler{store: store, config: config, records: make(map[string]Record), wake: make(chan struct{}, 1), done: make(chan struct{}), stop: make(chan struct{})}, nil
}

func NewScheduler(store JobStore, config Config) (*Scheduler, error) { return New(store, config) }
func (s *Scheduler) Restore(ctx context.Context, pluginID string, accountID int64) error {
	records, err := s.store.List(ctx, pluginID, accountID, s.config.Maximum)
	if err != nil {
		atomic.AddUint64(&s.restoreFailures, 1)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if record.Status.Active > 0 {
			record.Status.LastRun.Status = protocol.JobRunInterrupted
			record.Status.State = protocol.JobStateInterrupted
			record.Status.Active = 0
			atomic.AddUint64(&s.interrupted, 1)
			_ = s.persistLocked(ctx, record)
		}
		s.records[key(record.Spec.Identity)] = record
		if record.Status.State == protocol.JobStateEnabled && record.Status.NextRunUnixMS > 0 {
			heap.Push(&s.queue, &item{key: key(record.Spec.Identity), at: time.UnixMilli(record.Status.NextRunUnixMS)})
		}
	}
	return nil
}
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("jobs: scheduler already running")
	}
	s.running = true
	s.mu.Unlock()
	go s.loop(ctx)
	return nil
}
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	close(s.stop)
	s.running = false
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Scheduler) loop(ctx context.Context) {
	s.loopAlive.Store(true)
	defer func() { s.loopAlive.Store(false); close(s.done) }()
	for {
		wait := s.nextWait()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			s.RunDue(ctx)
		case <-s.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-s.stop:
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}
func (s *Scheduler) nextWait() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return time.Hour
	}
	wait := time.Until(s.queue[0].at)
	if wait < 0 {
		return 0
	}
	return wait
}
func (s *Scheduler) RunDue(ctx context.Context) {
	now := s.config.Clock.Now()
	for {
		s.mu.Lock()
		if len(s.queue) == 0 || s.queue[0].at.After(now) {
			s.mu.Unlock()
			return
		}
		entry := heap.Pop(&s.queue).(*item)
		record, ok := s.records[entry.key]
		if !ok || record.Status.State != protocol.JobStateEnabled || record.Status.NextRunUnixMS != entry.at.UnixMilli() {
			s.mu.Unlock()
			continue
		}
		err := s.fireLocked(ctx, record, entry.at, now)
		s.mu.Unlock()
		if err != nil {
			continue
		}
	}
}
func (s *Scheduler) Create(ctx context.Context, spec protocol.JobSpec) (protocol.JobStatus, error) {
	if err := spec.Validate(); err != nil {
		return protocol.JobStatus{}, err
	}
	now := s.config.Clock.Now()
	first, err := initialRun(spec, now)
	if err != nil {
		return protocol.JobStatus{}, err
	}
	if first.After(now.Add(time.Duration(spec.DeadlineMS)*time.Millisecond)) && spec.Schedule.Kind == protocol.JobScheduleOneShot {
		return protocol.JobStatus{}, fmt.Errorf("%w: one-shot exceeds deadline horizon", protocol.ErrInvalidDTO)
	}
	spec.Revision = 1
	status := protocol.JobStatus{Identity: spec.Identity, State: protocol.JobStateEnabled, Revision: 1, NextRunUnixMS: first.UnixMilli()}
	record := Record{Spec: spec, Status: status}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) >= s.config.Maximum {
		return protocol.JobStatus{}, ErrCapacity
	}
	if _, exists := s.records[key(spec.Identity)]; exists {
		return protocol.JobStatus{}, ErrDuplicate
	}
	if spec.Persistent {
		if err := s.store.Create(ctx, record); err != nil {
			return protocol.JobStatus{}, err
		}
	}
	s.records[key(spec.Identity)] = record
	heap.Push(&s.queue, &item{key: key(spec.Identity), at: first})
	s.signal()
	return status, nil
}
func (s *Scheduler) Update(ctx context.Context, request protocol.JobUpdateRequest) (protocol.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(request.Identity)]
	if !ok {
		return protocol.JobStatus{}, ErrNotFound
	}
	if request.Revision != record.Status.Revision {
		return protocol.JobStatus{}, ErrRevision
	}
	if request.Schedule != nil {
		record.Spec.Schedule = *request.Schedule
	}
	if request.Timezone != nil {
		record.Spec.Timezone = *request.Timezone
	}
	if request.DeadlineMS != nil {
		record.Spec.DeadlineMS = *request.DeadlineMS
	}
	if request.MaxRuntimeMS != nil {
		record.Spec.MaxRuntimeMS = *request.MaxRuntimeMS
	}
	if request.Misfire != nil {
		record.Spec.Misfire = *request.Misfire
	}
	if request.Overlap != nil {
		record.Spec.Overlap = *request.Overlap
	}
	if err := record.Spec.Validate(); err != nil {
		return protocol.JobStatus{}, err
	}
	record.Status.Revision++
	record.Spec.Revision = record.Status.Revision
	next, err := initialRun(record.Spec, s.config.Clock.Now())
	if err != nil {
		return protocol.JobStatus{}, err
	}
	record.Status.NextRunUnixMS = next.UnixMilli()
	if record.Spec.Persistent {
		if err := s.store.Update(ctx, record, request.Revision); err != nil {
			return protocol.JobStatus{}, err
		}
	}
	s.records[key(request.Identity)] = record
	heap.Push(&s.queue, &item{key: key(request.Identity), at: next})
	s.signal()
	return record.Status, nil
}
func (s *Scheduler) Action(ctx context.Context, identity protocol.JobIdentity, revision uint64, action protocol.JobState) (protocol.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok {
		return protocol.JobStatus{}, ErrNotFound
	}
	if revision != 0 && revision != record.Status.Revision {
		return protocol.JobStatus{}, ErrRevision
	}
	if action != protocol.JobStatePaused && action != protocol.JobStateEnabled && action != protocol.JobStateCancelled {
		return protocol.JobStatus{}, errors.New("jobs: invalid state action")
	}
	record.Status.State = action
	record.Status.Revision++
	record.Spec.Revision = record.Status.Revision
	if action == protocol.JobStateCancelled {
		record.Status.NextRunUnixMS = 0
	}
	if record.Spec.Persistent {
		if err := s.store.Update(ctx, record, record.Status.Revision-1); err != nil {
			return protocol.JobStatus{}, err
		}
	}
	s.records[key(identity)] = record
	s.signal()
	return record.Status, nil
}

func (s *Scheduler) fireLocked(ctx context.Context, record Record, scheduled, now time.Time) error {
	if now.Sub(scheduled) > time.Duration(record.Spec.DeadlineMS)*time.Millisecond {
		record.Status.Missed++
		if record.Spec.Misfire == protocol.JobMisfireSkip {
			record.Status.LastRun = attempt(record, scheduled, protocol.JobRunOverdue, now)
			s.scheduleNextLocked(&record, scheduled, now)
			return s.saveRuntime(ctx, record)
		}
	}
	if record.Status.Active > 0 {
		switch record.Spec.Overlap {
		case protocol.JobOverlapSkipIfRunning:
			record.Status.Skipped++
			record.Status.LastRun = attempt(record, scheduled, protocol.JobRunSkipped, now)
			return s.saveRuntime(ctx, record)
		case protocol.JobOverlapCoalesceOnePending:
			record.Status.Coalesced++
			return s.saveRuntime(ctx, record)
		case protocol.JobOverlapQueueOneBounded:
			if record.Status.Active >= 2 {
				record.Status.Skipped++
				return s.saveRuntime(ctx, record)
			}
		}
	}
	runID := fmt.Sprintf("%s-%d", record.Spec.Identity.ID, record.Status.Runs+record.Status.Rejected+1)
	record.Status.Active++
	record.Status.Runs++
	record.Status.LastRun = attempt(record, scheduled, protocol.JobRunAccepted, now)
	invocation := protocol.JobInvocation{Identity: record.Spec.Identity, Handler: record.Spec.Handler, Payload: record.Spec.Payload, ScheduledAtUnixMS: scheduled.UnixMilli(), RunID: runID}
	run := Run{Invocation: invocation}
	run.Complete = func(err error) { s.complete(record.Spec.Identity, record.Spec.Revision, scheduled, now, runID, err) }
	err := s.config.Executor.TrySubmit(record.Spec.Identity.PluginID, executor.Job{ID: runID, HandlerID: record.Spec.Handler, Kind: executor.KindScheduled, Payload: run, Enqueued: now, Timeout: time.Duration(record.Spec.MaxRuntimeMS) * time.Millisecond})
	if err != nil {
		record.Status.Active--
		record.Status.Rejected++
		record.Status.LastRun.Status = protocol.JobRunRejected
		record.Status.LastRun.Error = &protocol.JobErrorSummary{Category: protocol.JobErrorOverloaded, Message: "plugin executor rejected scheduled work", Retryable: true}
		return s.saveRuntime(ctx, record)
	}
	s.scheduleNextLocked(&record, scheduled, now)
	s.records[key(record.Spec.Identity)] = record
	return s.saveRuntime(ctx, record)
}

func (s *Scheduler) scheduleNextLocked(record *Record, scheduled, now time.Time) {
	if record.Spec.Schedule.Kind == protocol.JobScheduleOneShot || record.Spec.Schedule.Kind == protocol.JobScheduleFixedDelay {
		record.Status.NextRunUnixMS = 0
		return
	}
	next, err := nextAfterSubmit(record.Spec, scheduled, now)
	if err != nil {
		record.Status.NextRunUnixMS = 0
		return
	}
	for !next.After(now) {
		next, err = nextAfterSubmit(record.Spec, next, now)
		if err != nil {
			record.Status.NextRunUnixMS = 0
			return
		}
	}
	record.Status.NextRunUnixMS = next.UnixMilli()
	heap.Push(&s.queue, &item{key: key(record.Spec.Identity), at: next})
}
func (s *Scheduler) complete(identity protocol.JobIdentity, revision uint64, scheduled, queued time.Time, runID string, runErr error) {
	now := s.config.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok || record.Spec.Revision != revision {
		return
	}
	if record.Status.Active > 0 {
		record.Status.Active--
	}
	record.Status.LastRun.FinishedAtUnixMS = now.UnixMilli()
	record.Status.LastRun.RuntimeMS = now.Sub(queued).Milliseconds()
	record.Status.LastRun.RunID = runID
	if runErr == nil {
		record.Status.LastRun.Status = protocol.JobRunCompleted
	} else if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, executor.ErrHandlerTimeout) {
		record.Status.LastRun.Status = protocol.JobRunTimedOut
		record.Status.LastRun.Error = &protocol.JobErrorSummary{Category: protocol.JobErrorTimeout, Message: truncateError(runErr.Error()), Retryable: false}
	} else {
		record.Status.LastRun.Status = protocol.JobRunFailed
		record.Status.LastRun.Error = &protocol.JobErrorSummary{Category: protocol.JobErrorHandler, Message: truncateError(runErr.Error()), Retryable: false}
	}
	record.History = append(record.History, record.Status.LastRun)
	if len(record.History) > 16 {
		record.History = record.History[len(record.History)-16:]
	}
	if record.Spec.Schedule.Kind == protocol.JobScheduleFixedDelay && record.Status.State == protocol.JobStateEnabled {
		if next, err := nextRun(record.Spec, now); err == nil {
			record.Status.NextRunUnixMS = next.UnixMilli()
			heap.Push(&s.queue, &item{key: key(identity), at: next})
		}
	}
	_ = s.persistLocked(context.Background(), record)
	s.records[key(identity)] = record
	s.signal()
}
func (s *Scheduler) saveRuntime(ctx context.Context, record Record) error {
	s.records[key(record.Spec.Identity)] = record
	if err := s.persistLocked(ctx, record); err != nil {
		return err
	}
	return nil
}
func (s *Scheduler) persistLocked(ctx context.Context, record Record) error {
	if !record.Spec.Persistent {
		return nil
	}
	if err := s.store.Update(ctx, record, record.Status.Revision); err != nil {
		if errors.Is(err, ErrRevision) {
			return nil
		}
		atomic.AddUint64(&s.storeFailures, 1)
		return err
	}
	return nil
}
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Scheduler) Close(ctx context.Context) error { return s.Stop(ctx) }
func initialRun(spec protocol.JobSpec, now time.Time) (time.Time, error) {
	if spec.Schedule.Kind == protocol.JobScheduleOneShot {
		return time.UnixMilli(spec.Schedule.OneShot.RunAtUnixMS), nil
	}
	return nextRun(spec, now)
}
func nextAfterSubmit(spec protocol.JobSpec, scheduled, now time.Time) (time.Time, error) {
	if spec.Schedule.Kind == protocol.JobScheduleFixedRate {
		next := scheduled.Add(time.Duration(spec.Schedule.Interval.IntervalMS) * time.Millisecond)
		for !next.After(now) {
			next = next.Add(time.Duration(spec.Schedule.Interval.IntervalMS) * time.Millisecond)
		}
		return next, nil
	}
	return nextRun(spec, scheduled)
}
func attempt(record Record, scheduled time.Time, status protocol.JobRunStatus, now time.Time) protocol.JobExecutionAttempt {
	return protocol.JobExecutionAttempt{RunID: fmt.Sprintf("%s-%d", record.Spec.Identity.ID, record.Status.Runs), Status: status, ScheduledAtUnixMS: scheduled.UnixMilli(), StartedAtUnixMS: now.UnixMilli(), QueueDelayMS: now.Sub(scheduled).Milliseconds()}
}
func truncateError(message string) string {
	if len(message) > 512 {
		return message[:512]
	}
	return message
}
func key(identity protocol.JobIdentity) string {
	return fmt.Sprintf("%s/%d/%s", identity.PluginID, identity.AccountID, identity.ID)
}
