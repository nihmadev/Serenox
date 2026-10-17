package v1

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExpandedClientRequestGoldenAndBounds(t *testing.T) {
	request := ClientRequest{Operation: ClientGetHistory, PeerID: 42, Limit: 25, Query: "archive", Direction: "older"}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ClientRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalidRequest := range []ClientRequest{
		{Operation: ClientGetHistory, PeerID: 42, Limit: MaxClientPageItems + 1},
		{Operation: ClientGetMessages, PeerID: 42},
		{Operation: ClientInspectMedia, Handle: ""},
		{Operation: ClientSearchMessages, PeerID: 42, Query: string(make([]byte, MaxClientQueryBytes+1))},
	} {
		if err := invalidRequest.Validate(); !errors.Is(err, ErrInvalidDTO) {
			t.Fatalf("%#v: %v", invalidRequest, err)
		}
	}
}

func TestExpandedClientResultRejectsNestedOverflow(t *testing.T) {
	result := ClientResult{Messages: []Message{{ID: 1, ChatID: 2, Entities: make([]Entity, MaxClientEntities+1)}}}
	if err := result.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("expected nested entity limit, got %v", err)
	}
	result = ClientResult{Page: &PageInfo{Cursor: string(make([]byte, MaxClientCursorBytes+1))}}
	if err := result.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("expected cursor limit, got %v", err)
	}
}
