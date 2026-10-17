# Autopilot

Autopilot (`example.autopilot`) is Serenox's flagship bounded-workflow example.
It combines owner commands, message watchers, atomic host storage, durable host
jobs, typed Telegram action results, optional control-bot approvals, optional
allowlisted HTTP, and a process-exit recovery demonstration. It is not a script
runner: definitions contain only a finite trigger and up to 16 typed steps.

## Quick start

From the repository root:

```sh
go test ./examples/plugins/autopilot/...
mkdir -p bin dist
go build -o bin/serenox ./cmd/serenox
bin/serenox plugin build \
  --source examples/plugins/autopilot \
  --output dist/example.autopilot-1.0.0.srnx
bin/serenox plugin verify dist/example.autopilot-1.0.0.srnx
bin/serenox plugin install dist/example.autopilot-1.0.0.srnx
```

The `build` command compiles `bin/autopilot`, fills the package file table, and
packs it. Do not commit the generated binary or archive.

In Telegram:

```text
.autopilot demo install
.autopilot demo run
.flow list
.flow runs demo-reminder
.flow audit
```

The reminder sends a typed notification, suspends in a persistent one-shot host
job, resumes, sends a second notification, and completes. Reinstalling the demo
does not duplicate definitions or scheduled jobs.

## Commands

```text
.autopilot help
.autopilot status
.autopilot pause | resume
.autopilot demo install | demo run
.autopilot cleanup
.autopilot chaos status
.autopilot chaos fail-job confirm
.autopilot chaos interrupt-run confirm
.autopilot chaos exit-process confirm

.flow list
.flow show <workflow-id>
.flow enable | disable <workflow-id>
.flow trigger <workflow-id>
.flow cancel | retry <run-id>
.flow approve | reject <run-id>
.flow runs [workflow-id]
.flow audit [run-id]
.flow export <workflow-id>
.flow import <strict-json-definition>
.flow delete <workflow-id> confirm
```

All ordinary commands are owner-only. Output is bounded and never includes
message bodies, HTTP response bodies, callback tokens, or internal stack traces.

The optional control bot exposes one owner-only private command,
`/autopilot`, plus expiring approval buttons. If the bot is absent, approval
runs remain waiting and the owner uses `.flow approve` or `.flow reject`.

## Configuration

| Key | Default | Meaning |
|---|---:|---|
| `chaos_enabled` | `false` | Enables only the exact confirmed Autopilot chaos commands. |
| `control_bot_enabled` | `false` | Attempts owner approval UI through an attached control bot. |
| `http_enabled` | `false` | Enables the signed connector catalog. |

The only packaged connector is credential-free, read-only
`demo-status -> https://example.com/`. A workflow supplies its alias and a
bounded path, never an arbitrary origin. Authenticated connectors are
unsupported because reviewed secret-valued plugin configuration is not yet a
complete product surface.

## Definition example

Dates are RFC 3339. Step indexes are zero-based; `next: 0` means the next
sequential step, and explicit branches must point forward.

```json
{"version":"autopilot.workflow/v1","id":"incident-followup","name":"Incident follow-up","description":"Notify after a durable delay","enabled":true,"revision":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","trigger":{"kind":"manual"},"steps":[{"id":"start","kind":"notify","text":"Run {{run.id}} started"},{"id":"wait","kind":"wait","delay_ms":60000},{"id":"done","kind":"complete","text":"follow-up complete"}],"limits":{"concurrency":1,"cooldown_ms":0,"retention":16}}
```

Unknown fields, unknown variables, invalid UTF-8/JSON, backward branches,
cycles, oversized data, and unsupported trigger/step kinds fail closed.

## Hard limits

- 32 workflow definitions, 16 steps each, 32 transitions per run;
- 64 retained runs and 256 audit summaries total;
- names 64 bytes, descriptions 256 bytes, definitions 32 KiB;
- run context 16 KiB and rendered text 4,096 bytes;
- regex text 256 bytes, RE2-compatible Go regular expressions;
- one waiting job and one waiting approval per run;
- SDK runtime: 2 dispatch workers and queue capacity 32;
- host package policy: 1 HTTP request at a time, 4 KiB response, 3-second
  timeout, no redirects, exact HTTPS origin.

These values are plugin policy. The daemon and protocol may enforce stricter
limits. Package-v1 resource fields are currently Python-only, so native Go
worker/queue bounds live in the executable's SDK configuration and are stated
here rather than placed in an invalid manifest field.

## Crash/recovery demo

Enable `chaos_enabled` in reviewed plugin configuration, then:

```text
.autopilot chaos exit-process confirm
.ping
.plugins
# wait for supervisor backoff/restart
.flow runs chaos-recovery
.flow audit
```

Autopilot commits a `running` marker before exiting with status 23. The daemon,
Telegram transport, and other plugins are not signalled. On the first dispatch
after supervisor restart, Autopilot loads state and converts the prepared run
to `interrupted`. Recovery is deliberately lazy because protocol-v1 lifecycle
callbacks cannot issue reverse host RPC before the dispatch loop is active.

Do not use this as an OS-sandbox demonstration. `.srnx` verification and RPC
permissions constrain package identity and host services; the native process is
still ordinary code under the daemon's operating-system account.

## Failure semantics

- Dispatch is at-most-once. Autopilot deduplicates a bounded set of message IDs.
- Storage commits are atomic; Telegram/HTTP and storage are not one transaction.
- A required action advances only after a typed `success` result.
- `outcome_unknown` is recorded as failed and is never retried automatically.
- Read-only HTTP failures may be manually retried as a new run. Writes would be
  ambiguous and are not present in the shipped connector catalog.
- A duplicate/stale job or approval cannot advance a run twice.
- Active runs become `interrupted` after process exit. Valid persistent waiting
  jobs survive and are inspected; missing/cancelled waits become `interrupted`.
- Schema v1 is read-compatible across 1.x. An unknown or inconsistent schema
  refuses service instead of deleting state. Roll back only to a version that
  understands the stored schema.

See [the full architecture and operator guide](../../../docs/autopilot.md).
