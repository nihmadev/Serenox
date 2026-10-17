package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/executor"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type fakeExecutor struct {
	jobs []executor.Job
	err  error
}

func (e *fakeExecutor) TrySubmit(_ string, job executor.Job) error {
	if e.err != nil {
		return e.err
	}
	e.jobs = append(e.jobs, job)
	return nil
}

func testSpec(clock *fakeClock) protocol.JobSpec {
	return protocol.JobSpec{Identity: protocol.JobIdentity{PluginID: "plugin", AccountID: 1, ID: "maintenance"}, Handler: "run", Schedule: protocol.JobSchedule{Kind: protocol.JobScheduleFixedRate, Interval: &protocol.IntervalSchedule{IntervalMS: 1000}}, Timezone: protocol.JobTimezone{Name: "UTC"}, Persistent: true, DeadlineMS: 24 * 60 * 60 * 1000, MaxRuntimeMS: 1000, Misfire: protocol.JobMisfireSkip, Overlap: protocol.JobOverlapSkipIfRunning, Payload: []byte(`{"version":1}`), Revision: 0}
}
func TestSchedulerUsesOneBoundedExecutorSubmission(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	exec := &fakeExecutor{}
	scheduler, err := New(NewMemoryStore(4), Config{Clock: clock, Executor: exec})
	if err != nil {
		t.Fatal(err)
	}
	status, err := scheduler.Create(context.Background(), testSpec(clock))
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 1 {
		t.Fatalf("revision = %d", status.Revision)
	}
	clock.Advance(time.Second)
	scheduler.RunDue(context.Background())
	if len(exec.jobs) != 1 || exec.jobs[0].Kind != executor.KindScheduled {
		t.Fatalf("submitted jobs = %#v", exec.jobs)
	}
}
func TestSchedulerRejectsFullPluginQueue(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	exec := &fakeExecutor{err: errors.New("full")}
	scheduler, _ := New(NewMemoryStore(4), Config{Clock: clock, Executor: exec})
	_, _ = scheduler.Create(context.Background(), testSpec(clock))
	clock.Advance(time.Second)
	scheduler.RunDue(context.Background())
	status, err := scheduler.Get(context.Background(), protocol.JobIdentity{PluginID: "plugin", AccountID: 1, ID: "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Rejected != 1 || status.Active != 0 || status.LastRun.Status != protocol.JobRunRejected {
		t.Fatalf("status = %#v", status)
	}
}

func TestSchedulerSkipsExpiredMisfire(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	scheduler, err := New(NewMemoryStore(4), Config{Clock: clock, Executor: &fakeExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(clock)
	spec.DeadlineMS = 1000
	if _, err := scheduler.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Second)
	scheduler.RunDue(context.Background())
	status, err := scheduler.Get(context.Background(), spec.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if status.Missed != 1 || status.LastRun.Status != protocol.JobRunOverdue || status.Active != 0 {
		t.Fatalf("status = %#v", status)
	}
}
