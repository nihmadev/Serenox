package v1

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodePayloadRejectsTrailingValue(t *testing.T) {
	t.Parallel()
	var value map[string]any
	if err := DecodePayload(json.RawMessage(`{"ok":true} {"second":true}`), &value); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("trailing payload error = %v", err)
	}
}

func TestMetadataValidation(t *testing.T) {
	t.Parallel()
	metadata := PluginMetadata{
		ID: "echo", Name: "Echo", Version: "1.2.3", ProtocolVersion: Version,
		Commands: []CommandSpec{{Name: "echo"}},
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("valid metadata: %v", err)
	}
	metadata.Commands = append(metadata.Commands, CommandSpec{Name: "echo"})
	if err := metadata.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("duplicate command error = %v", err)
	}
}

func TestDispatchValidationRequiresTypedPayload(t *testing.T) {
	t.Parallel()
	request := DispatchRequest{Update: Update{ID: "1", Kind: UpdateMessage}}
	if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("missing message error = %v", err)
	}
	request.Update.Message = &Message{ID: 1, ChatID: 2, Text: "hello"}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
}

func TestEventDTOValidationBoundsQueriesAndCallbacks(t *testing.T) {
	t.Parallel()
	query := Update{ID: "inline", Kind: UpdateInlineQuery, InlineQuery: &InlineQuery{ID: "q", SenderID: 7}}
	if err := query.Validate(); err != nil {
		t.Fatalf("valid inline query: %v", err)
	}
	query.InlineQuery.ID = string(make([]byte, MaxQueryIDBytes+1))
	if err := query.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized query ID error = %v", err)
	}
	callback := Update{ID: "callback", Kind: UpdateCallback, Callback: &Callback{ID: "c", SenderID: 7, Data: string(make([]byte, MaxCallbackDataBytes+1))}}
	if err := callback.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized callback data error = %v", err)
	}
}

func TestMetadataValidatesInlineAndCallbackDeclarations(t *testing.T) {
	t.Parallel()
	metadata := PluginMetadata{
		ID: "inline", Name: "Inline", Version: "1", ProtocolVersion: Version,
		Capabilities:     []Capability{CapabilityInline},
		InlineHandlers:   []InlineSpec{{Name: "search", QueryPrefix: "go"}},
		CallbackHandlers: []CallbackSpec{{Name: "button", DataPrefix: "btn:"}},
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("valid event metadata: %v", err)
	}
	metadata.CallbackHandlers[0].Data = string(make([]byte, MaxCallbackDataBytes+1))
	if err := metadata.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized callback declaration error = %v", err)
	}
}
