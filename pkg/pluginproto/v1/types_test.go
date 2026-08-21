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
