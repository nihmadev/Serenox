package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventPublishStrictRoundTripAndUnknownField(t *testing.T) {
	original := EventPublishRequest{Type: "example.note.created", Version: 1, DedupeKey: "note-1", Payload: json.RawMessage(`{"text":"hello"}`)}
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := MarshalPayload(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EventPublishRequest
	if err := DecodePayload(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != original.Type || string(decoded.Payload) != string(original.Payload) {
		t.Fatalf("round trip = %#v", decoded)
	}
	if err := DecodePayload(json.RawMessage(`{"type":"example.note.created","version":1,"payload":{},"source":"forged"}`), &decoded); err == nil {
		t.Fatal("unknown trusted identity field was accepted")
	}
}

func TestEventPayloadBoundsAndNaming(t *testing.T) {
	tests := []EventPublishRequest{
		{Type: "Example.Note", Version: 1, Payload: json.RawMessage(`{}`)},
		{Type: "example.note", Version: 0, Payload: json.RawMessage(`{}`)},
		{Type: "example.note", Version: 1, Payload: json.RawMessage(`{"x":`)},
		{Type: "example.note", Version: 1, Payload: json.RawMessage(`{"x":"` + strings.Repeat("a", MaxEventStringBytes+1) + `"}`)},
	}
	for index, test := range tests {
		if err := test.Validate(); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
	value := "{}"
	for i := 0; i <= MaxEventPayloadDepth; i++ {
		value = `{"x":` + value + `}`
	}
	if err := (EventPublishRequest{Type: "example.note", Version: 1, Payload: json.RawMessage(value)}).Validate(); err == nil {
		t.Fatal("excessive payload depth accepted")
	}
}

func TestEventDeclarationCapabilitiesAndPermissions(t *testing.T) {
	publication := EventPublication{Type: "example.note.created", Version: 1, MaxPayloadBytes: 1024, DeliveryClass: EventDurable}
	metadata := PluginMetadata{ID: "example.producer", Name: "producer", Version: "1", ProtocolVersion: Version, EventPublications: []EventPublication{publication}}
	if err := metadata.Validate(); err == nil {
		t.Fatal("publication without capability accepted")
	}
	metadata.Capabilities = []Capability{CapabilityEventsPublish}
	if err := metadata.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := EventPermission("publish", publication.Type, 1); got != "event.publish.example.note.created.v1" {
		t.Fatalf("permission = %q", got)
	}
}
