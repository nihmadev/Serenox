// Command time is a minimal out-of-process Serenox plugin that implements
// the owner command .time.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

const currentTimePrefix = "Current time: "

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := serenox.Run(ctx, newHandler(time.Now), serenox.Config{
		Workers:      1,
		QueueSize:    8,
		WriteTimeout: 2 * time.Second,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "time plugin: %v\n", err)
		os.Exit(1)
	}
}

func newHandler(now func() time.Time) serenox.Handler {
	if now == nil {
		now = time.Now
	}
	return serenox.HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return protocol.PluginMetadata{
				ID:              "example.time",
				Name:            "Time",
				Version:         "1.0.0",
				Description:     "Replies with the current UTC time",
				Author:          "Serenox",
				ProtocolVersion: protocol.Version,
				Capabilities:    []protocol.Capability{protocol.CapabilityCommands},
				Commands: []protocol.CommandSpec{{
					Name:        "time",
					Description: "Show the current UTC time",
					OwnerOnly:   true,
				}},
			}, nil
		},
		DispatchFunc: func(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			if err := ctx.Err(); err != nil {
				return protocol.DispatchResponse{}, err
			}
			if request.Command == nil || request.Command.Name != "time" {
				return protocol.DispatchResponse{}, nil
			}
			return protocol.DispatchResponse{Actions: []protocol.Action{{
				ID:               "time-reply",
				Kind:             protocol.ActionReply,
				ChatID:           request.Command.Message.ChatID,
				ReplyToMessageID: request.Command.Message.ID,
				Text:             currentTimePrefix + now().UTC().Format(time.RFC3339Nano),
			}}}, nil
		},
	}
}
