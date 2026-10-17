package inline

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

type recordingBackend struct {
	mu      sync.Mutex
	created []Unit
	deleted []string
}

func (b *recordingBackend) Create(_ context.Context, unit Unit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created = append(b.created, unit)
	return nil
}
func (b *recordingBackend) Edit(_ context.Context, _ Unit) error { return nil }
func (b *recordingBackend) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, id)
	return nil
}

func newTestManager(t *testing.T, units int) *Manager {
	t.Helper()
	limits := DefaultLimits()
	limits.Units = units
	limits.DefaultTTL = 20 * time.Millisecond
	limits.MaxTTL = time.Second
	limits.SweepEvery = time.Millisecond
	manager, err := NewManager(limits, &recordingBackend{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestCreateCallbackAuthorizationAndCapacity(t *testing.T) {
	manager := newTestManager(t, 1)
	unit, err := manager.Create(context.Background(), CreateRequest{
		PluginID: "sample", OwnerUserID: 42, AllowedUsers: []int64{7}, Kind: KindForm,
		Text: "confirm", Buttons: [][]Button{{{Text: "OK", ActionID: "accept", Data: "yes"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), CreateRequest{PluginID: "other", OwnerUserID: 1, Kind: KindForm}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected capacity, got %v", err)
	}
	callback, err := manager.HandleCallback(unit.ID, "accept", 7, "typed")
	if err != nil || callback.PluginID != "sample" || callback.Data != "yes" {
		t.Fatalf("unexpected callback: %#v %v", callback, err)
	}
	if _, err := manager.HandleCallback(unit.ID, "accept", 9, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestValidationAndCopies(t *testing.T) {
	manager := newTestManager(t, 2)
	if _, err := manager.Create(context.Background(), CreateRequest{PluginID: "x", OwnerUserID: 1, Kind: KindList}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty list should be invalid: %v", err)
	}
	buttons := [][]Button{{{Text: "Link", URL: "https://example.test"}}}
	unit, err := manager.Create(context.Background(), CreateRequest{
		PluginID: "x", OwnerUserID: 1, Kind: KindList, Pages: []string{"one"}, Buttons: buttons,
	})
	if err != nil {
		t.Fatal(err)
	}
	buttons[0][0].Text = "mutated"
	got, ok := manager.Get(unit.ID)
	if !ok || got.Buttons[0][0].Text != "Link" {
		t.Fatalf("manager retained caller memory: %#v", got)
	}
}

func TestExpirationAndOwnedShutdown(t *testing.T) {
	before := runtime.NumGoroutine()
	manager := newTestManager(t, 2)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	unit, err := manager.Create(context.Background(), CreateRequest{PluginID: "x", OwnerUserID: 1, Kind: KindForm, TTL: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, ok := manager.Get(unit.ID); ok || manager.Len() != 0 {
		t.Fatal("expired unit retained")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if runtime.NumGoroutine() > before+1 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
	}
}
