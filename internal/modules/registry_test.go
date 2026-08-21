package modules

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type testModule struct {
	metadata    Metadata
	events      *[]string
	configureEr error
	startErr    error
	stopErr     error
}

func (m *testModule) Metadata() Metadata { return m.metadata }
func (m *testModule) Configure(context.Context, Services) error {
	*m.events = append(*m.events, "configure:"+m.metadata.ID)
	return m.configureEr
}
func (m *testModule) Start(context.Context) error {
	*m.events = append(*m.events, "start:"+m.metadata.ID)
	return m.startErr
}
func (m *testModule) Stop(context.Context) error {
	*m.events = append(*m.events, "stop:"+m.metadata.ID)
	return m.stopErr
}

func TestLifecycleDependencyOrderAndReverseStop(t *testing.T) {
	var events []string
	registry, err := NewRegistry(4, Services{})
	if err != nil {
		t.Fatal(err)
	}
	dependent := &testModule{metadata: Metadata{ID: "dependent", Name: "Dependent", Version: "1", Dependencies: []string{"base"}}, events: &events}
	base := &testModule{metadata: Metadata{ID: "base", Name: "Base", Version: "1"}, events: &events}
	if err := registry.Register(dependent); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(base); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"configure:base", "configure:dependent", "start:base", "start:dependent", "stop:dependent", "stop:base"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle order: got %v want %v", events, want)
	}
}

func TestStartFailureRollsBackAndSuspensionStaysUnregistered(t *testing.T) {
	var events []string
	registry, _ := NewRegistry(4, Services{})
	base := &testModule{metadata: Metadata{ID: "base", Name: "Base", Version: "1"}, events: &events}
	bad := &testModule{metadata: Metadata{ID: "bad", Name: "Bad", Version: "1", Dependencies: []string{"base"}}, events: &events, startErr: errors.New("boom")}
	if err := registry.Register(base); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(bad); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartAll(context.Background()); err == nil {
		t.Fatal("expected start error")
	}
	_, baseStatus, _ := registry.Lookup("base")
	_, badStatus, _ := registry.Lookup("bad")
	if baseStatus.State != StateStopped || badStatus.State != StateFailed {
		t.Fatalf("unexpected states: base=%s bad=%s", baseStatus.State, badStatus.State)
	}
	if !reflect.DeepEqual(events[len(events)-2:], []string{"start:bad", "stop:base"}) {
		t.Fatalf("rollback missing: %v", events)
	}
}

func TestCoreProtectionCapacityAndCycle(t *testing.T) {
	var events []string
	registry, _ := NewRegistry(2, Services{})
	core := &testModule{metadata: Metadata{ID: "core", Name: "Core", Version: "1", Core: true}, events: &events}
	if err := registry.Register(core); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(core); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected duplicate, got %v", err)
	}
	if err := registry.Unregister(context.Background(), "core"); !errors.Is(err, ErrCoreProtected) {
		t.Fatalf("expected core protection, got %v", err)
	}
	cycle := &testModule{metadata: Metadata{ID: "cycle", Name: "Cycle", Version: "1", Dependencies: []string{"missing"}}, events: &events}
	if err := registry.Register(cycle); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureAll(context.Background()); !errors.Is(err, ErrDependency) {
		t.Fatalf("expected dependency error, got %v", err)
	}
	extra := &testModule{metadata: Metadata{ID: "extra", Name: "Extra", Version: "1"}, events: &events}
	if err := registry.Register(extra); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected capacity, got %v", err)
	}
}

func TestConfigureSuspend(t *testing.T) {
	var events []string
	registry, _ := NewRegistry(1, Services{})
	module := &testModule{metadata: Metadata{ID: "sleeping", Name: "Sleeping", Version: "1"}, events: &events, configureEr: ErrSuspend}
	if err := registry.Register(module); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, _ := registry.Lookup("sleeping")
	if status.State != StateSuspended {
		t.Fatalf("unexpected state: %s", status.State)
	}
}
