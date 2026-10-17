package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestEventFabricComposesExternalPluginsAndDeduplicates(t *testing.T) {
	client := &runtimeClient{updates: make(chan pluginv1.Update), applied: make(chan struct{}, 1)}
	transport, err := telegram.NewClientTransport(client, telegram.ReconnectPolicy{Attempts: 1, Initial: time.Millisecond, Maximum: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(testOptions(t, transport))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitAppReady(t, application)
	publication := pluginv1.EventPublication{Type: "example.note.created", Version: 1, MaxPayloadBytes: 1024, DeliveryClass: pluginv1.EventDurable}
	subscription := pluginv1.EventSubscription{Type: publication.Type, Version: 1, HandlerID: "notes", SourcePolicy: pluginv1.EventSourceExact, SourcePlugin: "example.producer", DeliveryClass: pluginv1.EventDurable, MaxRuntimeMS: 1000, Retry: pluginv1.EventRetryPolicy{MaxAttempts: 2, InitialDelayMS: 10, MaxDelayMS: 100}, AcceptReplay: true}
	producer := pluginv1.PluginMetadata{ID: "example.producer", Name: "producer", Version: "1", ProtocolVersion: pluginv1.Version, Capabilities: []pluginv1.Capability{pluginv1.CapabilityEventsPublish}, EventPublications: []pluginv1.EventPublication{publication}}
	consumer := pluginv1.PluginMetadata{ID: "example.consumer", Name: "consumer", Version: "1", ProtocolVersion: pluginv1.Version, Capabilities: []pluginv1.Capability{pluginv1.CapabilityEventsSubscribe}, EventSubscriptions: []pluginv1.EventSubscription{subscription}}
	if err := application.RegisterExternal(ctx, producer, func(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
		return pluginv1.DispatchResponse{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	received := make(chan pluginv1.EventEnvelope, 2)
	if err := application.RegisterExternal(ctx, consumer, func(_ context.Context, request pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
		received <- request.Event.Envelope
		return pluginv1.DispatchResponse{EventResult: &pluginv1.EventHandlerResult{Outcome: pluginv1.EventAcknowledged}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	request := pluginv1.EventPublishRequest{Type: publication.Type, Version: 1, DedupeKey: "note-1", Payload: json.RawMessage(`{"text":"hello"}`)}
	first, err := application.HandleHostEvents(ctx, producer.ID, application.SelfID(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.HandleHostEvents(ctx, producer.ID, application.SelfID(), request)
	if err != nil || !second.Deduplicated || second.EventID != first.EventID {
		t.Fatalf("dedupe = %#v, %v", second, err)
	}
	select {
	case envelope := <-received:
		if envelope.Source != producer.ID || envelope.AccountID != application.SelfID() {
			t.Fatalf("envelope = %#v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event delivery timed out")
	}
	select {
	case <-received:
		t.Fatal("deduplicated event was delivered twice")
	case <-time.After(250 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
