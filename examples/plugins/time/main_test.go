package main

import (
	"context"
	"errors"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestMetadataAndTimeDispatch(t *testing.T) {
	fixed := time.Date(2026, time.August, 21, 10, 11, 12, 345000000, time.FixedZone("test", 4*60*60))
	handler := newHandler(func() time.Time { return fixed })

	metadata, err := handler.Metadata(context.Background())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("metadata validation: %v", err)
	}
	if metadata.ID != "example.time" || len(metadata.Commands) != 1 || metadata.Commands[0].Name != "time" || !metadata.Commands[0].OwnerOnly {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}

	request := timeRequest("time")
	response, err := handler.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
	if len(response.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(response.Actions))
	}
	action := response.Actions[0]
	if action.Kind != protocol.ActionReply || action.ChatID != 42 || action.ReplyToMessageID != 7 {
		t.Fatalf("unexpected action target: %#v", action)
	}
	want := currentTimePrefix + "2026-08-21T06:11:12.345Z"
	if action.Text != want {
		t.Fatalf("action text = %q, want %q", action.Text, want)
	}
}

func TestTimeDispatchIgnoresOtherCommandsAndHonorsCancellation(t *testing.T) {
	handler := newHandler(time.Now)
	response, err := handler.Dispatch(context.Background(), timeRequest("other"))
	if err != nil || len(response.Actions) != 0 {
		t.Fatalf("other command response = %#v, err = %v", response, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = handler.Dispatch(ctx, timeRequest("time"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dispatch error = %v", err)
	}
}

func timeRequest(command string) protocol.DispatchRequest {
	message := protocol.Message{ID: 7, ChatID: 42, SenderID: 9, DateUnixMS: 1, Text: "." + command, Outgoing: true}
	return protocol.DispatchRequest{
		Update: protocol.Update{
			ID: "update-1", Kind: protocol.UpdateMessage, OccurredUnixMS: 1,
			Message: &message,
		},
		Command: &protocol.Command{Name: command, Prefix: ".", Message: message},
	}
}
