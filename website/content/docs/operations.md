---
title: Production operations and troubleshooting
description: Operate, diagnose, recover, and shut down a Serenox daemon safely.
---

# Operations

## Startup and shutdown

The declarative control plane is local and explicit. Validate with
`serenox manifest check`, inspect with `serenox config effective`, and use
`serenox run --manifest serenox.yaml` to enter the same existing daemon
lifecycle. A manifest does not start services declared in `services`; systemd,
Compose, or another external manager owns those processes.

The process owns Telegram admission, module workers, inline expiration,
resource sampling, plugin processes, local sockets, and storage. On `SIGINT` or
`SIGTERM`, admission stops first, queues drain within their deadline, plugin
lifecycle stop is attempted, remaining child process groups are terminated,
sockets are removed, and storage is closed.

Use an external service manager with a stop timeout greater than the configured
shutdown deadline. Never send `SIGKILL` during normal deployment because it
prevents the final storage flush and socket cleanup.

## Backpressure

Every plugin has a fixed queue and worker count. Commands may wait only for the
configured enqueue timeout; watcher updates are rejected immediately on a full
queue. Queue saturation is isolated to that plugin and is exposed through the
metrics snapshot and diagnostics command.

Increase a plugin's queue only after measuring its sustained handler latency.
Increasing workers can increase Telegram/API pressure, so pair it with an
appropriate rate limit.

## Process plugins

The supervisor negotiates protocol v1 over a mode-restricted Unix socket.
Malformed or oversized frames disconnect that plugin. Crashes use bounded
exponential restart backoff and a finite retry budget. Repeated failures leave
the plugin stopped/degraded while the core continues.

Plugin executables should:

1. connect only to the socket supplied by the core;
2. complete the handshake before the configured deadline;
3. honor request contexts and lifecycle stop;
4. write bounded diagnostic messages to stderr;
5. never daemonize or spawn unmanaged descendants.

Python packages follow the same lifecycle. The supervisor authenticates the
local socket, validates handshake/configure/start/health, bounds stdout/stderr,
owns the process group, removes the runtime directory, and applies restart
backoff plus a circuit breaker. Diagnostics include interpreter and SDK/sidecar
versions, package version, process state, restart/circuit state, queue depth,
workers, RSS, and the last error.

Install only `.srnx` artifacts through `serenox plugin` or the daemon loader. Both
paths use the same verifier, trust policy, staging directory, active-version
record, and retention bound. A failed upgrade restores the previous active
record and process; `serenox plugin rollback <id>` also permits an explicit
operator rollback. Package verification never starts archive content.

The running daemon exposes a local, mode-0600 control socket at
`<data_dir>/.serenox/control.sock` for `serenox plugin load`. Its rotated token
is removed together with the socket during shutdown. The endpoint waits until
the application is ready before accepting a load, so a development package can
be tested immediately after daemon startup without a restart.

## Resource diagnosis

The diagnostics surface reports goroutine count, heap bytes, current RSS, peak
RSS, queue depth/capacity, worker activity, rejected admissions, rate-limiter
identities/evictions, plugin restart state, and component health. Labels are
bounded; update IDs and user IDs are log fields rather than metric labels.

When investigating growth, run the benchmark/resource suite and compare its
machine-readable result with [`benchmarks.md`](benchmarks.md). A soak is passing
only when goroutine count returns to baseline and RSS/heap reach a stable band.

## Recovery and rollback

New storage is separate from legacy JSON. Stop Serenox before restoring a new
store backup. The legacy userbot remains independently runnable and is not
modified by import or validation. See [`migration.md`](migration.md).

Media cleanup is owned by the daemon media service. Keep transfer metrics
bounded and redact handles, filenames, captions, tokens, and content; see
[`media-api.md`](media-api.md) for lifecycle and exact limits.

## Live moderation verification

Normal test and recorded modes are non-destructive and prove no live Telegram
behavior. Moderation mappings remain `live-gated` unless an operator supplies
a disposable admin account, disposable chat/forum/channel, and consenting
disposable target, performs a read-only preflight, and records successful
cleanup. Never point a future live fixture at self, a creator, or a production
chat. Current status is in
[moderation-implementation-matrix.md](moderation-implementation-matrix.md).
# Event Fabric operations

Durable records live in the host store namespace `core.event_fabric.v1`. Do not edit that namespace manually. Startup validates every record and reports corrupt record IDs. Use `serenox events status`, `list`, `inspect`, `retry EVENT_ID --subscription PLUGIN/HANDLER`, and `discard EVENT_ID --yes`; these operations use the authenticated local control socket and never print payloads or free-form error text.
