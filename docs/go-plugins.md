# External Go plugin authoring

External Go plugins are executables, not Go `plugin` shared objects. This keeps
their crashes, memory, dependencies, and runtime version outside the core.

## Contract

Import `github.com/nihmadev/Serenox/pkg/pluginproto/v1` for DTOs and
`github.com/nihmadev/Serenox/sdk/go` for the server loop. A plugin declares
metadata, commands, watcher filters, configuration schema, and capabilities in
its handshake. It then receives lifecycle and dispatch calls.

```go
package main

import (
    "context"

    protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
    serenox "github.com/nihmadev/Serenox/sdk/go"
)

func main() {
    handler := serenox.HandlerFuncs{
        MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
            return protocol.PluginMetadata{
                ID:              "example.ping",
                Name:            "Ping",
                Version:         "1.0.0",
                ProtocolVersion: protocol.Version,
                Capabilities:    []protocol.Capability{protocol.CapabilityCommands},
                Commands: []protocol.CommandSpec{{
                    Name:        "ping",
                    Description: "Replies with pong",
                }},
            }, nil
        },
        DispatchFunc: func(_ context.Context, req protocol.DispatchRequest) (protocol.DispatchResponse, error) {
            if req.Command == nil || req.Command.Name != "ping" {
                return protocol.DispatchResponse{}, nil
            }
            return protocol.DispatchResponse{Actions: []protocol.Action{{
                ID:               "ping-" + req.Update.ID,
                Kind:             protocol.ActionReply,
                ChatID:           req.Command.Message.ChatID,
                ReplyToMessageID: req.Command.Message.ID,
                Text:             "pong",
            }}}, nil
        },
    }
    if err := serenox.Run(context.Background(), handler, serenox.Config{}); err != nil {
        panic(err)
    }
}
```

`MetadataFunc` is required. The other lifecycle callbacks have bounded no-op
defaults, except a missing `DispatchFunc` returns the stable `unsupported`
protocol error. A complete compiling and tested example lives in
[`examples/plugins/echo`](../examples/plugins/echo).

## Lifecycle and connection

The core creates the local endpoint before starting the executable and injects:

- `SERENOX_PLUGIN_SOCKET` — owned local Unix socket path;
- `SERENOX_PLUGIN_TOKEN` — per-process authentication token;
- `SERENOX_PLUGIN_INSTANCE_ID` — bounded instance identifier;
- `SERENOX_PLUGIN_PROTOCOL` — requested protocol version;
- `SERENOX_PLUGIN_MAX_FRAME_BYTES` — frame-size ceiling.

`serenox.Run` connects to that endpoint, authenticates the handshake, and serves
`Configure`, `Start`, `Dispatch`, `Health`, and `Stop`. The SDK defaults to two
dispatch workers, a queue of 64 jobs, a 1 MiB frame limit, and a five-second
write timeout. Authors may provide smaller explicit limits through
`serenox.Config`; valid worker and queue ranges are 1–64 and 1–4096.

Metadata and package identity/version should agree. Commands and watcher
filters are declared during handshake. The core applies watcher filters before
admitting work to a plugin process.

## Rules

- Protocol version `1.0` is the exact version negotiated by the current SDK;
  optional DTO fields remain forward-compatible within the versioned contract.
- Telegram-library objects never cross the socket.
- One response matches one request ID. Frames over the negotiated maximum are
  rejected.
- Return action DTOs rather than contacting the account session directly.
- Honor context cancellation and finish `Stop` promptly.
- Keep internal work bounded; core queue limits do not bound queues created by a
  plugin itself.
- Do not retain Telegram-library types. The protocol exposes stable message,
  media, command, update, configuration, action, and error DTOs.

Configuration arrives in the configure lifecycle call, and inline work is
returned as action DTOs. Reverse storage and client-operation RPC method names
are reserved in protocol v1, but the current Go SDK does not expose a
plugin-initiated reverse-RPC client. Code must treat an unavailable operation as
unsupported rather than opening a second control channel. A handler must assume
at-most-once dispatch delivery: the core does not replay a call after a crash
because earlier actions may already have taken effect.

## Build and package

Build one executable for every declared platform. Static binaries are preferred
when the plugin has no native-library requirement:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o /tmp/package/bin/plugin-linux-amd64 .
bin/serenox plugin pack \
  --source /tmp/package \
  --manifest manifest.json \
  --output example.plugin-1.0.0.srnx
bin/serenox plugin verify example.plugin-1.0.0.srnx
```

Distribute the resulting `.srnx`, not a loose executable and not a Go `.so`.
See [the package format and trust workflow](plugin-packages.md) for the exact
manifest, signing, installation, and rollback contracts.
