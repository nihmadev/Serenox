---
title: Compatibility and implementation status
description: Check verified, partial, planned, and unsupported Serenox surfaces.
---

# Compatibility matrix: no vague “mostly supported” claims

Serenox is compatible by surface, not by slogan. A row below describes the
current protocol/runtime contract and points to the evidence that supports it.

| Status | Meaning |
|---|---|
| **verified** | Covered by passing unit, integration, recorded, or sidecar fixtures. |
| **partial** | A bounded subset works; a live adapter, broader API, or acceptance gate is still open. |
| **planned** | The shape exists or is intended, but there is no passing compatibility fixture yet. |
| **unsupported** | Rejected explicitly, with no claim of success. |

## Legacy behavior versus Serenox behavior

| Surface | Status | What is supported now | What is intentionally different |
|---|---|---|---|
| Owner command prefix | Verified | One- and multi-character prefixes, case folding, arguments | Legacy prefix slicing bugs are not reproduced |
| Commands and aliases | Verified | Owner-only commands, aliases, core-command protection | Core commands cannot be replaced by external plugins |
| Watchers | Verified | Direction, chat, media, text, reply, mention, sender, command-state filters | Filters run before plugin queue admission |
| Queues and overload | Verified | Per-plugin queues, fixed workers, bounded rejection | No detached task per accepted event |
| Storage | Verified | Namespaced JSON, atomic commits, bounded keys/values/file size | No synchronous whole-database rewrite API |
| Application manifest | Partial | Strict versioned YAML, redacted effective config, migration, service checks, release records, backups, systemd/Compose rendering | PostgreSQL, Redis leases, S3, encrypted secret backups, and live daemon release activation are not enabled |
| Python lifecycle | Verified | Decorators, configure/start/stop/health, restart and unload | Python code runs in a supervised sidecar process |
| Python storage | Verified | Async `get/set/delete` through authenticated host RPC | Sync cache helpers are not a durability promise |
| Durable jobs | Partial | Typed one-shot, interval, calendar, trigger, pause/resume, inspect, clear, and delete API with bounded persistence | Live plugin restart/account-unload fixtures and benchmark/soak evidence remain open; no exactly-once claim |
| Autopilot workflows | Partial | Strict finite schema, durable state/jobs, typed action results, command fallback approvals, and process-scoped chaos tests | Full installed recorded/live crash rehearsal and pre-ready reverse-RPC recovery remain operator gates |
| Text actions | Verified | Send, reply, edit, delete DTOs and adapter mapping | Telegram batches are sequential, never transactional |
| Action results | Partial | Typed success/failure/unknown/skipped states and scoped in-memory idempotency | Real-account acceptance and exactly-once delivery are not claimed |
| Profile name / `profile.clock` | Partial | Typed first/last-name action, lease ownership, recorded adapter, Go/Python helpers, durable minute job, and cleanup hooks | Live-account mutation and crash-after-acceptance rehearsal remain operator gates; Telegram is not transactional |
| Media | Partial | Bounded inspect/download/upload/attach/delete bridge and Go/Python clients | Live-account media smoke remains an operator gate |
| HTTP | Verified | Allowlisted outbound HTTP with finite bytes, headers, redirects, time, and rate | No sockets, proxy passthrough, cookies, retries, or inbound listener |
| Inline/client surface | Partial | Typed domain groups, bounded scoped pagination, identity/peer/message reads, and explicit unsupported outcomes | Dialog, participant, and broader search live mappings remain adapter gates; broad arbitrary Telegram client API is unavailable |
| Forms/lists/galleries | Partial | Bounded protocol model and validation | Live Telegram adapter is not verified |
| Raw Telegram objects | Unsupported | Use explicit protocol DTOs | gotd/Pyrogram/Aiogram objects never cross the process boundary |
| Runtime `pip` / `import_lib` | Unsupported | Package dependencies with the artifact | Startup never installs code from the network |
| Terminal / shell modules | Unsupported | Host HTTP and typed host services only | No unrestricted command execution |
| Full Hikka compatibility | Unsupported | Migrate behavior-by-behavior using this table | No blanket compatibility claim before fixture coverage |

## Telegram verification boundary

The live Go adapter is compiled and covered by adapter tests. The repository also
contains an opt-in smoke test that can verify a disposable account's identity,
updates, send, HTML edit, reply linkage, and deletion with cleanup. It does not
run in normal `go test ./...` because it needs credentials and a target peer.

```sh
SERENOX_LIVE_TELEGRAM=1 \
  go test ./internal/telegram -run TestLiveTelegramSmoke -count=1
```

That gate does not turn Telegram mutations into transactions. A lost connection
after a send can still produce an `outcome_unknown` result; recovery requires a
typed read or an application-level record.

The complete read operation matrix is maintained in
[telegram-client-matrix.md](telegram-client-matrix.md). Recorded fixtures
verify DTO shape, limits, cursor isolation, and deterministic behavior; they do
not upgrade an operation to live-verified status.

## Known fixed incompatibilities

The rewrite intentionally corrects several legacy failure modes: ineffective
chat rate counters, per-event decay timers, detached watcher tasks, retained
zero counters, non-atomic database writes, and unbounded inline callback/media
state. Modules that depended on those bugs need a migration, not a compatibility
flag.

## Moderation compatibility

The moderation surface is additive protocol v1, not raw Telethon/Pyrogram/
Hikka compatibility. Migrations must replace library calls with typed SDK
groups and explicit permissions. Recorded fixtures are never evidence of live
support. Exact live-gated, partial, recorded-only, and unsupported states are
listed in [moderation-implementation-matrix.md](moderation-implementation-matrix.md).
# Event protocol compatibility

Protocol v1 Event Fabric fields are additive. Exact event type plus positive schema version is the compatibility key. Wildcard subscriptions and unknown fields are rejected. Local file-backed durability is verified; multi-node delivery, consensus, high availability, and exactly-once effects are unsupported.
