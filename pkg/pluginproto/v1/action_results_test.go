package v1

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestActionResultRoundTripAndBounds(t *testing.T) {
	result := ActionResult{ActionID: "send-1", Operation: "send", Status: ActionResultSuccess, Success: true,
		MessageID: 42, DateUnixMS: 1000, MediaHandle: "media-1", Metadata: map[string]string{"source": "recorded"}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ActionResult
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ActionID != result.ActionID || decoded.Operation != result.Operation || decoded.MessageID != 42 {
		t.Fatalf("decoded=%#v", decoded)
	}
	if err := (ActionResult{ActionID: "x", Operation: "send", Status: ActionResultOutcomeUnknown, OutcomeUnknown: true, Error: &Error{Code: ErrorOutcomeUnknown, Message: "timeout"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ActionResult{ActionID: "x", Operation: "send", Status: ActionResultFailure}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("missing failure error=%v", err)
	}
	if err := (ActionResult{ActionID: "x", Operation: "send", Status: ActionResultSuccess, Success: true, Metadata: map[string]string{"too_big": string(make([]byte, MaxActionMetadataValueBytes+1))}}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("metadata bound=%v", err)
	}
}

func TestActionBatchValidationAndStopOnError(t *testing.T) {
	actions := []Action{{ID: "one", Kind: ActionSend, ChatID: 1, Text: "one"}, {ID: "two", Kind: ActionSend, ChatID: 1, Text: "two"}}
	request := ActionBatchRequest{Actions: actions, StopOnError: true}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ActionBatchRequest{Actions: []Action{actions[0], actions[0]}}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("duplicate action=%v", err)
	}
	if err := (ActionBatchResponse{Stopped: true}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("stopped non-partial=%v", err)
	}
}
