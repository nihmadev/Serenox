---
title: Telegram moderation API
description: Use typed, permission-gated moderation operations through the host.
---

# Telegram moderation API

Serenox protocol v1 exposes moderation only through `host.moderation`. Plugins
never receive gotd values, access hashes, raw methods, sessions, or a generic
parameter blob. The Go and Python SDKs expose the same seven groups: members,
admins, messages, settings, join requests, invites, and topics.

## Targets and reply safety

A chat is a numeric transport-neutral ID plus an optional expected kind. A
participant target contains exactly one of a numeric user ID, public username,
or replied-message reference. The core reads the replied message and accepts
only a visible user sender. Missing/deleted/service messages return
`moderation_target_unavailable`; anonymous administrators return
`moderation_anonymous_sender`; channel-as-sender posts return
`moderation_channel_sender`. Forward metadata is never substituted for the
actual sender.

The bounded peer LRU owns access hashes learned from validated update, read,
participant, invite, and full-chat entity sets. Numeric targets must already
be resolvable. Username targeting performs one explicit
`contacts.resolveUsername` lookup. The cache defaults to 4,096 entries, never
fabricates a hash, and is destroyed with the Telegram transport.

## Rights and deadlines

`DeniedRights` has direct semantics: present `true` means denied, present
`false` means explicitly allowed, and omission means preserve the existing
value. Only the gotd adapter converts it to Telegram's inverted
`ChatBannedRights`. `AdminRights` is also direct: present `true` grants and
present `false` removes a right. Ownership transfer is absent.

Deadlines are UTC Unix milliseconds. `permanent=true` requires
`until_unix_ms=0`; a restriction with both `permanent=false` and a zero
deadline is rejected as ambiguous. A finite restriction/invite deadline must be from 30 seconds
through 366 days in the future; past, overflow, and Telegram's implicit
“permanent” threshold values are rejected. `unmute` clears only the
participant-specific banned-rights object and does not change chat defaults.
Effective permissions remain the intersection of defaults, participant
restrictions, membership, and Telegram server policy.

The SDK `mute` preset lowers to one explicit restriction denying text, media,
photos, video, round video, voice, audio, documents, stickers, GIFs, games,
inline bots, link previews, polls, and plain messages. It is not a second
backend method.

## Results, ordering, and replay

Results use `success`, `failure`, `skipped`, `noop`, `partial`, or
`outcome_unknown`, with stable structured errors. An acknowledgement does not
fabricate a resulting member snapshot. Transport loss after a mutation may
have reached Telegram and is conservatively `outcome_unknown`; it is never
automatically retried.

Mutations can carry a maximum 128-byte idempotency key. Results are scoped by
plugin, account, operation, chat, target selector, and key in a bounded 4,096
entry/10-minute in-memory LRU. Ambiguous results are retained too. The store is
not durable across daemon restart and is cleared on transport close. At most
16 service requests (8 in the live and 4 in recorded adapters) run
concurrently. Mutations affecting the same account/chat/target are serialized
through a lifecycle-owned table capped at 512 active keys and four
holder/waiter references per key; entries disappear immediately after the
last release. Different chats and targets continue independently. There is no
per-chat goroutine or timer.

The global semaphore is paired with a bounded per-plugin/account admission
gate allowing two active backend-bound requests per scope, so one plugin
cannot occupy all live moderation slots. The 4,096-entry gate retains entries
only while requests are active and rejects excess work instead of queueing it.

Each plugin/account/chat/operation-group bucket permits four tokens per second
with a burst of 20 and a bounded 8,192-identity table. A 100-message batch
costs fewer than five tokens. Capacity and waiter pressure return structured,
retryable `rate_limited` or `overloaded` failures before Telegram invocation.

An optional `expected_state` token performs one bounded member read before a
targeted mutation and rejects a stale reversal with
`moderation_state_conflict`. Scheduled reconciliation belongs in the durable
Jobs API; Telegram's restriction deadline remains authoritative. Jobs are
at-least-once and must use their own idempotency key and state token. Serenox
does not schedule an automatic unmute or claim exactly-once execution.

Mute plus command edit is sequential. A successful mute stays successful if
editing the owner's outgoing trigger fails; the example reports the edit as a
separate `notification_failed` error.

## Capability and permissions

Every operation requires capability `telegram.moderation` plus exactly the
mapped permission below. Package validation rejects unknown moderation
permissions and permissions without the capability; the supervisor checks the
mapping again immediately before the adapter call.

| Permission | Operations |
|---|---|
| `telegram.moderation.chat.read` | settings/pinned/topic chat-state reads |
| `telegram.moderation.members.read` | member get/list/search |
| `telegram.moderation.members.restrict` | restrict/update/unrestrict/mute/unmute |
| `telegram.moderation.members.ban` | ban/unban/kick |
| `telegram.moderation.admins.read` | acting/target admin rights and admin list |
| `telegram.moderation.admins.manage` | promote/update/demote/custom rank |
| `telegram.moderation.messages.delete` | administrative message deletion |
| `telegram.moderation.messages.pin` | pin/unpin one |
| `telegram.moderation.messages.unpin_all` | destructive unpin-all |
| `telegram.moderation.settings.manage` | defaults, slow mode, protected content |
| `telegram.moderation.join_requests.read` | pending-request list |
| `telegram.moderation.join_requests.manage` | approve/decline |
| `telegram.moderation.invites.read` | current account's invite list |
| `telegram.moderation.invites.manage` | create/edit/revoke invite |
| `telegram.moderation.topics.manage` | close/reopen/hide/unhide |
| `telegram.moderation.topics.delete` | destructive topic deletion |

## Chat-kind behavior

Member restrictions and administrator roles are live-mapped only for cached
megagroups/channels. Per-member send restrictions are rejected for broadcast
channels; ban/unban remains meaningful. Basic-group member/admin mutation is
explicitly unsupported in the current adapter, while default permissions,
protected content, message deletion, and pinning use the applicable
`messages.*` methods. Administrative deletion is rejected in private dialogs,
which retain the existing `telegram.delete` action. One-side pinning applies
only to private dialogs. Forum operations require a cached server entity with
`forum=true`. A linked discussion is targeted as its actual megagroup; the
adapter does not automatically discover, migrate, or rewire it.

Live join-request and invite lists currently expose one bounded page because
Telegram's cursor has two fields that protocol v1 does not yet represent.
Continuation returns `unsupported`; no hidden scan is performed. Member pages
use a signed, 15-minute, plugin/account/operation/chat/query-scoped cursor and
at most 20 pages.

Invite URLs are returned only by the authorized create/list/edit call. The
service never writes them to logs, metrics, errors, diagnostics, recorded
fixtures, or durable audit storage. Recorded mode intentionally returns empty
invite links.

## SDK examples

Go:

```go
moderation := plugin.ModerationFromContext(ctx)
chat := protocol.ModerationChatTarget{ChatID: message.ChatID, ExpectedKind: protocol.ModerationChatMegagroup}
target := plugin.ReplyTarget(message.ChatID, message.ReplyToMessageID)
result, err := moderation.Members.Mute(ctx, chat, target, time.Hour, "mute-command-42")
```

Python:

```python
chat = ChatTarget(message.chat_id, "megagroup")
target = ParticipantTarget.replied_message(message.chat_id, message.reply_to_message_id)
result = await self.moderation.members.mute(
    chat, target, duration_seconds=3600, idempotency_key=f"mute-{message.id}"
)
```

See `examples/plugins/moderation` for owner commands and exact outgoing,
reply-only, group-only `mute 1h` watcher parsing. Incoming messages
cannot reach that watcher, and localization remains entirely in the plugin.

## Limits and observability

Requests allow 100 message targets or page items, 16 warnings, 32-byte ranks
and invite names, 512-byte local reasons, a 1 MiB result frame, 30-second
default request lifetime, four backend calls per documented read, and no more
than 20 member pages. Existing bounded RPC, plugin-worker, metrics-series,
peer-cache, and shutdown limits remain in force. Aggregate service diagnostics
cover request/result categories, total latency, active/high-water requests,
rate-limit state, ordering entries, and idempotency hit/miss/eviction counts.
Metrics use only stable operation/result categories; IDs, usernames, ranks,
reasons, keys, message text, invite links, and raw RPC errors are prohibited as
labels.

Normal tests are credential-free and non-destructive. Live behavior is source
audited against gotd v0.145.1 but remains `live-gated` until an operator runs a
disposable-account verification fixture and restores every mutation.
