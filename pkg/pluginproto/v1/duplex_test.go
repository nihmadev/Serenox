package v1

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDuplexStorageDTOGolden(t *testing.T) {
	request := StorageRequest{Operation: StorageSet, Namespace: "settings", Key: "enabled", Value: json.RawMessage(`true`)}
	got, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"operation":"set","namespace":"settings","key":"enabled","value":true}`
	if string(got) != want {
		t.Fatalf("storage request JSON = %s, want %s", got, want)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}

	client := ClientRequest{Operation: ClientGetMessage, PeerID: 10, MessageID: 12}
	if err := client.Validate(); err != nil {
		t.Fatal(err)
	}
	result := ClientResult{Message: &Message{ID: 12, ChatID: 10, Text: "hello"}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDuplexDTORejectsMalformedAndOversizedInputs(t *testing.T) {
	tests := []StorageRequest{
		{Operation: StorageOperation("unknown"), Namespace: "n", Key: "k"},
		{Operation: StorageSet, Namespace: "n", Key: "k", Value: json.RawMessage(``)},
		{Operation: StorageBatch, Batch: make([]StorageMutation, MaxStorageBatchItems+1)},
	}
	for _, request := range tests {
		if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
			t.Fatalf("request %#v error = %v", request, err)
		}
	}
	if err := (ClientRequest{Operation: ClientGetMessage, PeerID: 1}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("malformed client request error = %v", err)
	}
}

func TestHTTPDTOGoldenAndLimits(t *testing.T) {
	request := HTTPRequest{Method: "GET", URL: "https://api.example.test/v1", Headers: []HTTPHeader{{Name: "accept", Value: "application/json"}}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"method":"GET","url":"https://api.example.test/v1","headers":[{"name":"accept","value":"application/json"}]}` {
		t.Fatalf("HTTP request JSON = %s", data)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (HTTPRequest{Method: "get", URL: "https://example.test"}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("lowercase method error = %v", err)
	}
	if err := (HTTPRequest{Method: "GET", URL: "https://example.test", Body: make([]byte, MaxHTTPRequestBytes+1)}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized body error = %v", err)
	}
	response := HTTPResponse{Status: 200, FinalURL: "https://api.example.test/v1", Body: []byte(`{"ok":true}`), ElapsedMS: 4}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (HTTPResponse{Status: 200, Rejected: true, Body: []byte("secret")}).Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("rejected body error = %v", err)
	}
}
