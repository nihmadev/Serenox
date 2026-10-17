---
title: Go plugin development
description: Build, test, package, install, and remove a supervised Go plugin.
---

# External Go plugin authoring

> New to Serenox plugin development? Start with the
> [complete author guide](plugin-development.md), then use this page as the
> protocol-level Go reference.

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
[`examples/plugins/echo`](../../../examples/plugins/echo).

## Lifecycle and connection

The core creates the local endpoint before starting the executable and injects:

- `SERENOX_PLUGIN_SOCKET` — owned local Unix socket path;
- `SERENOX_PLUGIN_TOKEN` — per-process authentication token;
- `SERENOX_PLUGIN_INSTANCE_ID` — bounded instance identifier;
- `SERENOX_PLUGIN_PROTOCOL` — requested protocol version;
- `SERENOX_PLUGIN_MAX_FRAME_BYTES` — frame-size ceiling.

`serenox.Run` connects to that endpoint, authenticates the handshake, and serves
`Configure`, `Start`, `Dispatch`, `Health`, and `Stop`. A handler that also
implements `BackgroundHandler` (`Background(ctx) error`) gets one long-running
goroutine started after `Start` succeeds, with host services attached to its
context, for reacting to sources outside Telegram such as D-Bus. Its context is
cancelled before `Stop` runs (host RPC is not served during `Stop`), and a
non-cancellation error closes the connection so the restart policy applies; see
`examples/plugins/now-playing`. The SDK defaults to two
dispatch workers, a queue of 64 jobs, a 1 MiB frame limit, and a five-second
write timeout. Authors may provide smaller explicit limits through
`serenox.Config`; valid worker and queue ranges are 1–64 and 1–4096.

Metadata and package identity/version should agree. Commands and watcher
filters are declared during handshake. Inline queries use bounded
`InlineSpec` declarations (`QueryPrefix` and `SenderIDs`); callback handlers
use `CallbackSpec` declarations (`Data`, `DataPrefix`, `ChatIDs`, and
`SenderIDs`). The core applies all of these filters before admitting work to a
plugin process, and callback routes created by action buttons expire after a
bounded lifetime.

Deleted-message updates are delivered as `UpdateDeleted` dispatch requests to
watchers that declare that update kind. They contain only the deleted message
IDs; Telegram library objects never cross the boundary.

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

The bounded media contract and typed `io.Reader`/`io.Writer` client are
documented in [media-api.md](media-api.md). No `[]byte` or filesystem-path
convenience API is supported.

Outbound HTTP is available only through `HTTPClientFromContext(ctx)` and the
typed `host.http` reverse-RPC contract. Declare `http` plus the manifest
permissions and signed origin policy described in [http-client.md](http-client.md);
the SDK never exposes `http.Client`, transports, cookies, proxies, or sockets.

Read-only Telegram access is available through `ClientFromContext(ctx)`. It is
split into typed groups, for example:

```go
client := plugin.ClientFromContext(ctx)
page, err := client.History.List(ctx, chatID, 50, "")
if err != nil { return protocol.DispatchResponse{}, err }
_ = page.Page.Cursor // persist only in the plugin's own storage namespace
message, err := client.Messages.Get(ctx, chatID, messageID)
```

Declare only the corresponding read permissions. A page cursor is opaque,
expiring, account/plugin scoped, and safe to persist as plugin data; it is not
a Telegram access hash or a raw offset object.

## Typed moderation

Use `plugin.ModerationFromContext(ctx)` and its `Members`, `Admins`,
`Messages`, `Settings`, `JoinRequests`, `Invites`, and `Topics` groups. Rights
and results are protocol DTOs; there is no raw Telegram client escape hatch.
The complete contract and examples are in [moderation-api.md](moderation-api.md).
# Event Fabric

Use `EventPublication`, `EventSubscription`, `EventsFromContext`, and `PublishJSON` from the Go SDK. Event handlers are registered by stable handler ID through `HandlerFuncs.EventFuncs`. Return `EventSuccess`, `EventRetry`, `EventReject`, or `EventOutcomeUnknown`. Durable handlers must be idempotent by event ID because a crash before acknowledgement can redeliver.
