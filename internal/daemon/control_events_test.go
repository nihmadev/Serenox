package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/eventbus"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestEventSummaryRedactsPayloadAndErrorText(t *testing.T) {
	record := eventbus.Record{Envelope: pluginv1.EventEnvelope{ID: "evt_1", Type: "example.note", Version: 1, Source: "example.producer", AccountID: 7, AcceptedUnixMS: 1, Payload: json.RawMessage(`{"secret":"do-not-print"}`)}, Class: pluginv1.EventDurable, Deliveries: []eventbus.Delivery{{ID: "example.consumer/notes", PluginID: "example.consumer", Subscription: pluginv1.EventSubscription{HandlerID: "notes"}, State: eventbus.StateDeadLettered, LastError: "failed: private diagnostic"}}}
	wire, err := json.Marshal(summarizeEvent(record))
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire)
	if strings.Contains(text, "do-not-print") || strings.Contains(text, "private diagnostic") {
		t.Fatalf("operator summary leaked private data: %s", text)
	}
	if !strings.Contains(text, `"last_error_code":"failed"`) {
		t.Fatalf("summary omitted stable error code: %s", text)
	}
}
