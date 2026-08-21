# Operations

## Startup and shutdown

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

Install only `.srnx` artifacts through `serenox plugin` or the daemon loader. Both
paths use the same verifier, trust policy, staging directory, active-version
record, and retention bound. A failed upgrade restores the previous active
record and process; `serenox plugin rollback <id>` also permits an explicit
operator rollback. Package verification never starts archive content.

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
