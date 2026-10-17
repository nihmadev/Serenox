// Command echo is a minimal out-of-process Serenox plugin.
package main

import (
	"context"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

func main() {
	if err := serenox.Run(context.Background(), newHandler(), serenox.Config{}); err != nil {
		panic(err)
	}
}

func newHandler() serenox.Handler {
	return serenox.HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return protocol.PluginMetadata{
				ID:              "example.echo",
				Name:            "Echo",
				Version:         "1.0.0",
				Description:     "Replies with the command arguments",
				Author:          "Serenox",
				ProtocolVersion: protocol.Version,
				Capabilities:    []protocol.Capability{protocol.CapabilityCommands},
				Commands: []protocol.CommandSpec{{
					Name:        "echo",
					Description: "Reply with the supplied text",
					OwnerOnly:   true,
				}},
			}, nil
		},
		DispatchFunc: func(_ context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			if request.Command == nil || request.Command.Name != "echo" {
				return protocol.DispatchResponse{}, nil
			}
			text := request.Command.RawArgs
			if text == "" {
				text = "echo"
			}
			return protocol.DispatchResponse{Actions: []protocol.Action{{
				ID:               "echo-" + request.Update.ID,
				Kind:             protocol.ActionReply,
				ChatID:           request.Command.Message.ChatID,
				ReplyToMessageID: request.Command.Message.ID,
				Text:             text,
			}}}, nil
		},
	}
}
