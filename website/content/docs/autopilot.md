---
title: Autopilot workflow guide
description: Study a durable, bounded workflow plugin from request through recovery.
---

# Autopilot: bounded workflows as an architecture proof

Autopilot is an installable supervised Go plugin (`example.autopilot@1.0.0`).
It is useful for small reminders, approvals, bounded message rules, scheduled
state updates, and reviewed read-only connectors. It was created instead of
another `.weather`-style example because one request/one response cannot show
the architectural value of durable jobs, atomic state, action outcomes,
process supervision, recovery, and least-privilege host services together.

The claim is not that equivalent business logic is impossible in Hikka. The
demonstration is narrower: a separately supervised process can fail without
terminating the daemon, its accepted work is bounded, delayed work belongs to
the daemon scheduler, permissions are inspected from a signed package, and
recovery produces explicit durable states.

## Architecture

```text
owner command / bounded message watcher / host schedule
                         |
                         v
              validated workflow snapshot
                         |
                         v
       pending -> running -> typed finite step
                         |       |       |
                         |       |       +-> host.http (exact origin)
                         |       +----------> host.actions (typed result)
                         +------------------> host.storage (atomic state)
                         |
              waiting_job / waiting_approval
                         |
                    host.jobs only
                         |
                         v
       completed / failed / cancelled / interrupted
```

The plugin creates no scheduler, per-workflow timer, worker pool, detached
goroutine tree, second database, socket, Telegram client, or filesystem state.
The Go SDK owns two fixed dispatch workers and a 32-item queue. Every mutation
is serialized by one in-process state coordinator and durably committed through
the host namespace.

## Audited Serenox surface

| Surface | Status for Autopilot | Evidence and exact boundary |
|---|---|---|
| Go metadata/lifecycle/dispatch | Verified | SDK handler contract and plugin tests; configure/start/health/stop are bounded. |
| Owner commands and message watcher | Verified | Protocol DTOs and executor routing are implemented; dynamic matching stays inside one declared watcher. |
| Durable jobs | Partial | create/get/delete and restore are implemented and unit-tested; a live process-restart/account-unload fixture remains an operator gate. |
| Atomic namespaced storage | Verified | `host.storage` maps to atomic core storage; the missing Go SDK client was added with tests. |
| Typed user-session results | Partial | result categories and idempotency are implemented; real-account acceptance remains smoke-gated. |
| Control-bot approvals | Partial/optional | owner routing and bot action DTOs are implemented; requires an attached, live bot. Command fallback is verified. |
| Host HTTP | Verified core, optional plugin use | Exact signed `https://example.com` origin; normal tests use a fake and never use public network. |
| Supervisor process exit | Verified by supervisor tests; plugin demo operator-gated | Non-zero child exit cannot signal the daemon; full `.ping` live rehearsal requires a running installation. |
| Recorded transport | Partial | deterministic core fixtures and plugin fake-host tests pass; a packaged recorded daemon rehearsal is not claimed as a live Telegram test. |
| Package preflight/build/verify | Verified when the commands in the release report pass | Manifest/process ID, version, and capabilities are checked again during activation. |
| Authenticated connector secrets | Unsupported | No reviewed secret-valued plugin configuration contract is used. |
| Pre-ready reverse-RPC recovery | Unsupported in protocol v1 | Recovery occurs on the first admitted command, watcher, callback, or job. |
| Arbitrary code/URL/Telegram API | Unsupported | No schema variant or SDK escape hatch exists. |

## Workflow schema

The definition version is `autopilot.workflow/v1`. A definition has a stable
ID, display metadata, enabled state, revision, timestamps, exactly one trigger,
an ordered finite step list, and optional concurrency/cooldown/retention policy.
The account/owner binding comes from admitted host DTOs; it is not accepted from
imported JSON.

Triggers:

- `manual` — `.flow trigger` or the demo command;
- `message` — incoming/outgoing plus up to three exact/prefix/suffix/contains/
  Go-RE2 matchers and optional 16-entry chat/sender scopes;
- `one_shot` — one persistent host job at an RFC 3339 instant;
- `interval` — fixed-delay host job, minimum one second;
- `calendar` — daily hour/minute in an explicit IANA timezone with DST reject.

Steps:

- `notify` — typed user-session send with a stable operation/idempotency ID;
- `wait` — persistent one-shot `resume_wait` job;
- `approval` — expiring owner decision with optional bot buttons and ordinary
  command fallback;
- `http` — connector alias, approved method, and bounded fixed-prefix path;
- `set` — JSON null/bool/number/string/object in bounded run context;
- `condition` — only `exists`, `eq`, or `neq` on `vars.<name>`;
- `complete` and `fail` — explicit terminal summaries.

Branches use higher zero-based step indexes. Omitted `next` proceeds to the
following step. Backward jumps, recursion, cycles, and paths over 32 transitions
are rejected. Every run retains an immutable definition snapshot and digest, so
editing a workflow cannot change an active run.

Allowed template variables are exactly `workflow.id`, `run.id`,
`message.text`, `message.chat_id`, `http.status`, `approval.decision`, and a
scalar `vars.<name>`. Parsing is single-pass: no functions, recursion,
evaluation, or non-scalar stringification.

## State, keys, and recovery

Autopilot stores one canonical JSON document at:

```text
host namespace: autopilot
key:            state-v1
schema:         1
```

The document contains `workflows`, `runs`, bounded `audit`, bounded message
deduplication, cooldown deadlines, paused state, and counters. One host `set`
atomically commits the complete invariant. The value is capped at the host's
1 MiB DTO limit, while plugin cardinality and per-object limits keep normal
state much smaller.

Run states are `pending`, `running`, `waiting_job`, `waiting_approval`,
`completed`, `failed`, `cancelled`, and `interrupted`. Each transition verifies
the current state, immutable revision, expected step, job identity or approval
identity, and the 32-transition budget under one mutex before committing.

On the first dispatch after start/restart:

1. strict-decode schema 1 and validate every index and cardinality;
2. mark `pending`/`running` runs `interrupted`;
3. inspect persistent waiting jobs and preserve only enabled identities;
4. mark missing, cancelled, interrupted, or uncommitted waiting jobs
   `interrupted`;
5. expire stale approvals;
6. retain ambiguous Telegram/HTTP outcomes without replaying them.

Unknown versions, malformed JSON, oversized state, inconsistent IDs, and
invalid run invariants fail closed. The operator should preserve the storage
file, reinstall a schema-compatible package, export recoverable definitions if
possible, and only then repair or migrate. Autopilot never silently resets the
namespace.

Schema 1 is the rollback boundary for 1.x. A future migration must be additive
or write a separately recoverable snapshot; activation must succeed before an
irreversible rewrite. Current v1 lifecycle cannot call reverse RPC during
`Start`, so health says ready with lazy reconciliation rather than making a
false pre-ready guarantee.

## Jobs

| Handler | Schedule/payload | Misfire and overlap |
|---|---|---|
| `workflow_schedule` | one-shot, fixed-delay, or calendar; workflow ID + revision | `fire_once`, `skip_if_running` |
| `resume_wait` | one-shot; run ID + revision + expected step | `fire_once`, `skip_if_running` |
| `approval_expiry` | one-shot; run ID + revision | `fire_once`, `skip_if_running` |
| `cleanup` | fixed-delay 24h; empty object | `fire_once`, `skip_if_running` |
| `chaos_failure` | confirmed one-shot; run ID + revision | intentional handler failure |

Payloads never contain definitions, message bodies, HTTP bodies, callback
tokens, audit histories, or secrets. Duplicate and stale deliveries increment
bounded counters and do not advance state. Core unload pauses owner jobs;
restore resumes them with the plugin's declared handlers. Job delivery is not
exactly once.

## Actions, idempotency, cancellation, retry

Each Telegram/control-bot/HTTP mutation uses a stable operation ID. A notify
persists its operation reference, calls `host.actions`, and advances only on
`success`. Core `accepted/queued` scheduling is not treated as Telegram
completion. `failure`, `skipped`, and `outcome_unknown` remain distinct.

The core's action idempotency journal is bounded and in-memory. It suppresses
duplicates inside its retention window but does not survive daemon restart.
Autopilot therefore also keeps durable run/operation state. It never retries an
ambiguous mutation. `.flow retry` creates a new run and is rejected when the
old run contains an unsafe or unknown mutation. A cancel racing an approval or
job resume is serialized: exactly one expected-state transition wins; the
loser is stale.

There is no transaction across Telegram, HTTP, jobs, and storage. A storage
commit can succeed before a host mutation fails, and an external mutation can
be accepted before a timeout. Audit records stable categories and safe summaries,
not response bodies.

## Permissions and security boundary

The manifest declares `commands`, `watchers`, `inline`, `storage`, `jobs`,
`http`, `control_bot`, and `control_bot.callbacks`. Permissions are limited to
`storage.read`, `storage.write`, `jobs.schedule`, `telegram.send`,
`telegram.reply`, `telegram.bot.write_ui`, and `network.http.read`.

The network policy allows only `https://example.com`, one concurrent request,
1 KiB requests, 4 KiB responses, three seconds, no redirect, 0.2 requests per
second, and burst one. The workflow cannot change the origin or headers.

These checks do not make an OS sandbox. A native executable remains native
code running as the daemon's OS user. Package verification establishes content
and signature policy; RPC permissions restrict calls through the Serenox host.
Use operating-system containment separately where hostile native code is in
scope.

## Modes and limitations

| Feature | Recorded | Live user session | Control bot | HTTP enabled |
|---|---|---|---|---|
| import/export, state machine, set/condition/terminal | Yes | Yes | Not required | Not required |
| manual/message triggers | Deterministic DTO path | Yes, adapter-gated | Not required | Not required |
| notify result | Recorded typed result | Partial: opt-in live smoke boundary | No | No |
| wait/schedule/cleanup | Host jobs | Host jobs | No | No |
| approval command fallback | Yes | Yes | Not required | No |
| approval buttons/dashboard | No credentials in normal tests | No | Optional/live-gated | No |
| `demo-status` connector | Fake only in normal tests | Same host service | No | Optional/public request |
| authenticated connector | Unsupported | Unsupported | Unsupported | Unsupported |
| arbitrary Telegram/API/code/shell | Unsupported | Unsupported | Unsupported | Unsupported |

Scheduled workflows have no originating chat. The shipped heartbeat is
state-only; a scheduled `notify` definition needs a future reviewed target
binding rather than a chat ID smuggled through imported text.

Persistent/local behavior depends on deployment storage. Ephemeral platforms
such as an unmounted container lose local data because of their filesystem
lifecycle; that is a deployment property, not a Serenox or Heroku feature.

## Operator troubleshooting

| Symptom | Meaning/action |
|---|---|
| `host storage service is unavailable` | Package permission/runtime bridge is missing; inspect manifest and daemon version. |
| `Autopilot is paused` | `.autopilot resume`; waiting jobs were left durable. |
| `workflow concurrency limit reached` | Inspect/cancel the active run; no hidden queue is created. |
| approval buttons do not appear | Attach/verify the control bot or use `.flow approve/reject`. |
| run becomes `interrupted` | Process exited active, or its waiting job could not be reconciled. Inspect audit before retry. |
| `outcome_unknown` | External mutation may have succeeded. Inspect Telegram/connector; do not blind-retry. |
| import names a field error | Correct the exact schema path; unknown fields are rejected. |
| plugin enters restart circuit | Stop issuing chaos exits, inspect daemon diagnostics, then reload after the restart window. |

For the complete command reference and copy-paste package path, see the
[English README](../../../examples/plugins/autopilot/README.md) or
[Russian README](../../../examples/plugins/autopilot/README.ru.md).
