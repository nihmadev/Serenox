package main

import (
	"context"
	"encoding/json"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

func main() {
	if err := serenox.Run(context.Background(), handler(), serenox.Config{}); err != nil {
		panic(err)
	}
}

func handler() serenox.Handler {
	publication := serenox.EventPublication("example.note.created", 1, 4096, protocol.EventDurable, "A deterministic example note")
	return serenox.HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return protocol.PluginMetadata{ID: "example.note.producer", Name: "Note Producer", Version: "1.0.0", ProtocolVersion: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityCommands, protocol.CapabilityEventsPublish}, Commands: []protocol.CommandSpec{{Name: "note", OwnerOnly: true}}, EventPublications: []protocol.EventPublication{publication}}, nil
		},
		DispatchFunc: func(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			if request.Command == nil {
				return protocol.DispatchResponse{}, nil
			}
			client := serenox.EventsFromContext(ctx)
			payload, _ := json.Marshal(map[string]string{"text": request.Command.RawArgs})
			_, err := client.Publish(ctx, protocol.EventPublishRequest{Type: publication.Type, Version: 1, DedupeKey: request.Update.ID, Payload: payload})
			return protocol.DispatchResponse{}, err
		},
	}
}
