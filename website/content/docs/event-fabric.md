---
title: Event Fabric
description: Route declared, bounded, durable events between supervised plugins.
---

# Serenox Event Fabric

The Event Fabric is a bounded, single-daemon event router for supervised Go and Python plugins. Plugins declare exact `type/version` publications and subscriptions in both runtime metadata and the signed `.srnx` manifest. They never discover processes, sockets, languages, or installation paths.

## Current event boundary audit

Before this subsystem, Telegram commands, watchers, inline queries, callbacks, control-bot requests, and job invocations were admitted to the fixed per-plugin executor and dispatched once. Dispatch transport failures were not replayed. Actions could finish in Telegram, HTTP, storage, media, moderation, or the control bot before the plugin process or RPC connection failed, so an error did not prove that the external effect had not happened. Process restart, unload, rollback, and daemon shutdown canceled or drained current executor work but did not preserve plugin-to-plugin work.

The Event Fabric does not change those legacy paths. Core Telegram updates are additionally normalized into declared events. A plugin that opts into both a legacy handler and an Event Fabric subscription will receive both by explicit choice.

## Envelope and declarations

`EventEnvelope` contains host-assigned `id`, `source`, `account_id`, acceptance time, attempt, and hop; publisher-provided type/version, occurrence time, correlation/causation/trace context, dedupe key, content type, and JSON payload; and no arbitrary headers. The host overwrites all identity and routing metadata. Types are exact lowercase namespaces such as `example.note.created/v1`; wildcards, prefixes, regexes, and runtime-created subscriptions are unsupported.

JSON is limited to 64 KiB, depth 16, 256 fields, 256 items per collection, and 16 KiB per string. A publication may set a lower signed payload maximum. Each plugin has at most 64 publications and 64 subscriptions, each event at most 32 deliveries, each handler response at most 16 emitted events, each causal chain at most 16 hops, and each trace at most 64 retained events.

Signed permissions are deterministic:

| Operation | Capability | Exact permission |
|---|---|---|
| Publish plugin event | `events.publish` | `event.publish.<type>.v<version>` |
| Consume event | `events.subscribe` | `event.subscribe.<type>.v<version>` |
| Core Telegram event | `events.subscribe` | exact `event.subscribe.telegram....v1`, with `source_policy: core` |
| Replay/DLQ administration | none for ordinary plugins | local operator API only |

Event permissions grant no storage, HTTP, media, jobs, Telegram mutation, moderation, bot, secret, or service authority. Handshake publications and subscriptions must equal the signed package declarations; activation stops and rolls back when they differ.

## Commit, delivery, and ordering

`host.events` validates identity, declaration, permission, frame/payload bounds, dedupe scope, retention, fan-out, and storage quota before responding. A durable event plus its entire initial fan-out is one JSON record and one atomic file-store commit. `accepted: true` means that commit completed; it does not mean a subscriber ran.

The durable delivery state machine is:

```text
pending -> running -> acknowledged
              |  \
              |   -> dead_lettered (terminal or ambiguous)
              -> retry_wait -> running
```

The application owns one bounded coordinator and admits deliveries through the existing per-plugin executor. Queue overload becomes a recorded retry/dead-letter transition. There is no event-specific goroutine, timer, lock, file, queue, or worker pool.

A successful handler return with `acknowledged` is acknowledgement. Retryable failures use deterministic exponential backoff with stable ±10% event-ID jitter and a signed maximum-attempt policy. Terminal failures dead-letter immediately. An ambiguous external mutation also dead-letters immediately and is never automatically retried. A crash after an effect but before acknowledgement can redeliver: durable delivery is at-least-once, not exactly-once. Use event ID or a publisher dedupe key in plugin-owned durable idempotency state.

For response-carried emissions, actions execute first and emitted events are accepted afterward. They share the same validator, permission, persistence, dedupe, and quota path as `host.events`. These operations are not transactional: action success can precede event publication failure. A child inherits trace/correlation context, records the parent as causation, and increments the host-owned hop.

Replay changes one compatible dead-lettered delivery back to pending; it does not create a new event or route to newly installed subscribers. The original envelope, source, schema version, account, and trace remain unchanged. Current registration, exact handler identity, permission, and `accept_replay` are rechecked. Discard is revision-checked. Retention expiry is deterministic.

On startup, records are strictly decoded and bounded. Pre-crash `running`/`admitted` states become interrupted retry-wait states before redelivery. Corrupt or incompatible records stop Event Fabric construction with the record ID in the diagnostic; state is never silently discarded. Unloaded plugins keep pending durable records but cannot claim work. Re-registration resumes compatible handlers. Uninstall policy/export UI remains planned; operators must currently retain state or use the internal bounded discard API.

## Core catalog and privacy

Verified core events are `telegram.message.received/v1`, `telegram.message.edited/v1`, `telegram.message.deleted/v1`, `telegram.callback.received/v1`, and `telegram.inline_query.received/v1`. Conversion accepts only protocol DTOs, never gotd objects, Bot API objects, access hashes, tokens, sessions, credentials, or stack traces. Control, job completion, and lifecycle catalog publication are partial and not enabled by default.

Metrics implementations receive only validated event type, installed plugin ID, state/result code, and delivery class. Payload, message text, IDs, correlation/trace IDs, dedupe keys, user/chat IDs, URLs, and error text are forbidden as labels. Operator list output is capped at 100 records. The authenticated local control socket exposes bounded, redacted `serenox events status`, `list`, `inspect`, `retry`, and confirmation-required `discard` commands.

## Implementation matrix

| Slice | Status | Notes |
|---|---|---|
| Protocol envelope, declarations, strict JSON validation | verified | Go golden/negative tests and Python DTO parity |
| Signed package policy and handshake equality | verified | exact declarations and permissions |
| Local durable acceptance and exact routing | verified | atomic event-plus-fanout record |
| Retry, DLQ, replay, expiry, stale revisions | verified | deterministic unit tests |
| Executor admission and lifecycle recovery | verified | existing executor; one owned coordinator |
| Go SDK | verified | typed publish, declaration and outcome helpers |
| Python SDK/sidecar | verified | typed declarations, decorator, publish and outcomes |
| Core Telegram catalog | partial | message/edit/delete/callback/inline only |
| Recorded cross-language fixtures | partial | real supervised Python consume/publish/ack fixture and Go application composition are verified; packaged two-process fixture remains planned |
| Operator CLI/control socket | verified | authenticated, bounded, redacted; discard requires `--yes` |
| Bounded event metrics | verified | injected metrics interface is wired to the application registry with finite type/plugin/result labels |
| Ephemeral at-most-once delivery | verified | one admission attempt; crash interruption is terminal and never redelivered |
| Multi-node delivery, consensus, HA, PostgreSQL | unsupported | single daemon and current file backend only |
| Exactly-once effects or cross-service transactions | unsupported | explicitly not claimed |

The future Service Broker may reuse envelope trace context, exact package policy, and executor admission, but synchronous service calls are not events and do not share delivery semantics.
