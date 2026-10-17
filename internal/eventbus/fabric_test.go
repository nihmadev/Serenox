package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/storage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct{ next string }

func (i *fakeIDs) NewEventID() string { return i.next }

type sequenceIDs struct{ next int }

func (i *sequenceIDs) NewEventID() string { i.next++; return fmt.Sprintf("evt_%032d", i.next) }

func testFabric(t *testing.T) (*Fabric, *fakeClock, *fakeIDs) {
	t.Helper()
	backend, err := storage.NewMemory(storage.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStorageStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0).UTC()}
	ids := &fakeIDs{next: "evt_00000000000000000000000000000001"}
	fabric, err := New(store, ids, clock, nil, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return fabric, clock, ids
}

func publication() protocol.EventPublication {
	return protocol.EventPublication{Type: "example.note.created", Version: 1, MaxPayloadBytes: 1024, DeliveryClass: protocol.EventDurable}
}
func subscription() protocol.EventSubscription {
	return protocol.EventSubscription{Type: "example.note.created", Version: 1, HandlerID: "notes", SourcePolicy: protocol.EventSourceAnyPublic, DeliveryClass: protocol.EventDurable, MaxRuntimeMS: 1000, Retry: protocol.EventRetryPolicy{MaxAttempts: 2, InitialDelayMS: 100, MaxDelayMS: 1000}, AcceptReplay: true}
}

func TestPublishOverwritesIdentityRoutesAndDeduplicates(t *testing.T) {
	fabric, _, _ := testFabric(t)
	pub := publication()
	sub := subscription()
	if err := fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}}); err != nil {
		t.Fatal(err)
	}
	if err := fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}}); err != nil {
		t.Fatal(err)
	}
	request := protocol.EventPublishRequest{Type: pub.Type, Version: 1, DedupeKey: "note-1", Payload: json.RawMessage(`{"text":"hello"}`)}
	result, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.MatchedSubscribers != 1 {
		t.Fatalf("result = %#v", result)
	}
	record, found, err := fabric.Get(context.Background(), result.EventID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if record.Envelope.Source != "example.producer" || record.Envelope.AccountID != 7 || record.Envelope.Attempt != 1 {
		t.Fatalf("host identity not assigned: %#v", record.Envelope)
	}
	duplicate, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, request)
	if err != nil || !duplicate.Deduplicated || duplicate.EventID != result.EventID {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
}

func TestDeliveryRetryDeadLetterReplayAndStaleRevision(t *testing.T) {
	fabric, clock, _ := testFabric(t)
	pub := publication()
	sub := subscription()
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
	accepted, _ := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	delivery, revision, err := fabric.Claim(context.Background(), accepted.EventID, "example.consumer/notes")
	if err != nil || delivery.Envelope.Attempt != 1 {
		t.Fatalf("claim: %#v %d %v", delivery, revision, err)
	}
	if err := fabric.Complete(context.Background(), accepted.EventID, "example.consumer/notes", revision+1, protocol.EventHandlerResult{Outcome: protocol.EventRetryable}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale completion = %v", err)
	}
	if err := fabric.Complete(context.Background(), accepted.EventID, "example.consumer/notes", revision, protocol.EventHandlerResult{Outcome: protocol.EventRetryable, Code: "crash"}); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Second)
	_, revision, err = fabric.Claim(context.Background(), accepted.EventID, "example.consumer/notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := fabric.Complete(context.Background(), accepted.EventID, "example.consumer/notes", revision, protocol.EventHandlerResult{Outcome: protocol.EventRetryable}); err != nil {
		t.Fatal(err)
	}
	record, _, _ := fabric.Get(context.Background(), accepted.EventID)
	if record.Deliveries[0].State != StateDeadLettered {
		t.Fatalf("state = %s", record.Deliveries[0].State)
	}
	if err := fabric.Replay(context.Background(), accepted.EventID, "example.consumer/notes"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountIsolationUndeclaredAndHopLimit(t *testing.T) {
	fabric, _, _ := testFabric(t)
	pub := publication()
	sub := subscription()
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 8, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
	result, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	if err != nil || result.MatchedSubscribers != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	_, err = fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: "example.other", Version: 1, Payload: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrNotDeclared) {
		t.Fatalf("undeclared = %v", err)
	}
	parent := protocol.EventEnvelope{ID: "evt_parent", TraceID: "trace", Hop: protocol.MaxEventHop}
	_, err = fabric.PublishDerived(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)}, parent)
	if !errors.Is(err, ErrHopLimit) {
		t.Fatalf("hop = %v", err)
	}
}

func TestRecoveryRedeliversDurableButNotEphemeral(t *testing.T) {
	for _, class := range []protocol.EventDeliveryClass{protocol.EventDurable, protocol.EventEphemeral} {
		t.Run(string(class), func(t *testing.T) {
			backend, _ := storage.NewMemory(storage.DefaultOptions())
			store, _ := NewStorageStore(backend)
			clock := &fakeClock{now: time.Unix(100, 0)}
			ids := &fakeIDs{next: "evt_00000000000000000000000000000002"}
			fabric, _ := New(store, ids, clock, nil, DefaultLimits())
			pub := publication()
			pub.DeliveryClass = class
			sub := subscription()
			sub.DeliveryClass = class
			if class == protocol.EventEphemeral {
				sub.Retry.MaxAttempts = 1
				sub.AcceptReplay = false
			}
			_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
			_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
			accepted, _ := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
			_, _, _ = fabric.Claim(context.Background(), accepted.EventID, "example.consumer/notes")
			_, err := New(store, ids, clock, nil, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			record, _, _ := store.Get(context.Background(), accepted.EventID)
			want := StateRetryWait
			if class == protocol.EventEphemeral {
				want = StateDeadLettered
			}
			if record.Deliveries[0].State != want {
				t.Fatalf("state = %s, want %s", record.Deliveries[0].State, want)
			}
		})
	}
}

func TestEphemeralRetryableOutcomeIsTerminal(t *testing.T) {
	fabric, _, _ := testFabric(t)
	pub := publication()
	pub.DeliveryClass = protocol.EventEphemeral
	sub := subscription()
	sub.DeliveryClass = protocol.EventEphemeral
	sub.Retry.MaxAttempts = 1
	sub.AcceptReplay = false
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
	accepted, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := fabric.Claim(context.Background(), accepted.EventID, "example.consumer/notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := fabric.Complete(context.Background(), accepted.EventID, "example.consumer/notes", revision, protocol.EventHandlerResult{Outcome: protocol.EventRetryable, Code: "busy"}); err != nil {
		t.Fatal(err)
	}
	record, _, _ := fabric.Get(context.Background(), accepted.EventID)
	if got := record.Deliveries[0].State; got != StateDeadLettered {
		t.Fatalf("state = %q, want %q", got, StateDeadLettered)
	}
}

func TestClaimRejectsRemovedSubscription(t *testing.T) {
	fabric, _, _ := testFabric(t)
	pub, sub := publication(), subscription()
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
	accepted, _ := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	fabric.Unregister("example.consumer", 7)
	if _, _, err := fabric.Claim(context.Background(), accepted.EventID, "example.consumer/notes"); !errors.Is(err, ErrPermission) {
		t.Fatalf("claim error = %v, want permission", err)
	}
}

func TestReadyScansPastOperatorDisplayPage(t *testing.T) {
	backend, _ := storage.NewMemory(storage.DefaultOptions())
	store, _ := NewStorageStore(backend)
	fabric, err := New(store, &sequenceIDs{}, &fakeClock{now: time.Unix(100, 0)}, nil, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	pub, sub := publication(), subscription()
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	for range 100 {
		if _, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = fabric.Register(Registration{PluginID: "example.consumer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsSubscribe}, Permissions: []string{protocol.EventPermission("subscribe", sub.Type, 1)}, Subscriptions: []protocol.EventSubscription{sub}})
	accepted, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := fabric.Ready(context.Background(), 100)
	if err != nil || len(ready) != 1 || ready[0].Envelope.ID != accepted.EventID {
		t.Fatalf("ready = %#v, err = %v", ready, err)
	}
}

func BenchmarkEventPublish(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fabric, _, ids := testFabric(&testing.T{})
		ids.next = "evt_00000000000000000000000000000001"
		pub := publication()
		_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
		_, _ = fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)})
	}
}

func TestSoakEventResourceBound(t *testing.T) {
	if os.Getenv("SERENOX_SOAK") != "1" {
		t.Skip("set SERENOX_SOAK=1")
	}
	backend, _ := storage.NewMemory(storage.DefaultOptions())
	store, _ := NewStorageStore(backend)
	limits := DefaultLimits()
	limits.MaxEventsPerMinute = 25
	fabric, err := New(store, &sequenceIDs{}, &fakeClock{now: time.Unix(100, 0)}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	pub := publication()
	_ = fabric.Register(Registration{PluginID: "example.producer", AccountID: 7, Capabilities: []protocol.Capability{protocol.CapabilityEventsPublish}, Permissions: []string{protocol.EventPermission("publish", pub.Type, 1)}, Publications: []protocol.EventPublication{pub}})
	for i := 0; i < limits.MaxEventsPerMinute; i++ {
		if _, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if _, err := fabric.Publish(context.Background(), Identity{PluginID: "example.producer", AccountID: 7}, protocol.EventPublishRequest{Type: pub.Type, Version: 1, Payload: json.RawMessage(`{}`)}); !errors.Is(err, ErrQuota) {
		t.Fatalf("over-limit publish = %v", err)
	}
}
