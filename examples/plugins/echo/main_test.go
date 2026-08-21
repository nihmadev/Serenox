package main

import (
	"context"
	"testing"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestMetadataAndEchoDispatch(t *testing.T) {
	handler := newHandler()
	metadata, err := handler.Metadata(context.Background())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("metadata validation: %v", err)
	}
	if metadata.ID != "example.echo" || len(metadata.Commands) != 1 || metadata.Commands[0].Name != "echo" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}

	request := protocol.DispatchRequest{
		Update: protocol.Update{
			ID: "update-1", Kind: protocol.UpdateMessage, OccurredUnixMS: 1,
			Message: &protocol.Message{ID: 7, ChatID: 42, Text: ".echo hello"},
		},
		Command: &protocol.Command{
			Name: "echo", Prefix: ".", RawArgs: "hello", Args: []string{"hello"},
			Message: protocol.Message{ID: 7, ChatID: 42, Text: ".echo hello"},
		},
	}
	response, err := handler.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
	if len(response.Actions) != 1 || response.Actions[0].Text != "hello" || response.Actions[0].ChatID != 42 {
		t.Fatalf("unexpected response: %#v", response)
	}
}
