---
title: Duplex RPC and error model
description: Reference authenticated host calls, deadlines, framing, and errors.
---

# Duplex host RPC

Protocol v1 uses the normal request/response envelope for reverse calls. Every
request has a non-zero `request_id` and an absolute `deadline_unix_ms`; the
connection is authenticated during the handshake. Reverse calls are bounded
by eight in-flight requests by default (the supervisor limit), a negotiated
frame limit, and the caller deadline.

The host storage DTOs are:

- `StorageRequest { operation, namespace, key, value, batch[] }`, where
  `operation` is `get`, `set`, `delete`, `list`, or `batch`.
- `StorageMutation { operation, namespace, key, value }` for atomic batch items.
- `StorageResult { found, value, entries[], committed }`.

Storage is committed synchronously before a successful `set`, `delete`, or
`batch` response. The core prefixes every namespace with plugin identity and
Telegram account identity. The store enforces 256 namespaces, 2048 keys per
namespace, 64K total entries, 128-byte namespaces, 256-byte keys, 1 MiB values,
and a 32 MiB file by default; batches are capped at 128 mutations.

The read DTOs are split into bounded identity, peer, user, chat, message,
history, dialog, participant, media, page, and structured-error values. The
explicit catalog includes `get_me`, `resolve_peer`, user/chat lookups, message
lookup, bounded history/search, dialog and participant reads, and media
metadata inspection. It is a finite catalog, not a raw Telegram-method
dispatcher. No gotd, Pyrogram, access hash, session, or other Telegram-library
value crosses the socket.

History and search pages use an opaque HMAC-authenticated cursor. It binds the
plugin, account, operation, peer, query hash, and direction; it expires after
15 minutes and contains no raw query, access hash, or Telegram offset object.
Page size is capped at 100 items and a cursor cannot be replayed by another
plugin or account. Ordering is stable best-effort; Telegram data may change
between pages. `has_more` comes from the bounded adapter response and never
triggers a hidden follow-up call.

Permissions are checked immediately before every reverse operation. Read
domains use `client.identity.read`, `client.peer.read`, `client.user.read`,
`client.chat.read`, `client.message.read`, `client.history.read`,
`client.search.read`, `client.dialogs.read`, `client.participants.read`, and
`client.media.metadata`; legacy `client.<operation>` names remain a v1 alias.
Malformed/oversized requests, expired cursors, deadlines, missing/deleted
entities, disconnects, and unsupported adapter operations return stable
structured errors, never an empty successful page.

The sidecar bounds its pending reverse map and semaphore, removes waiters on
cancellation/disconnect, and reports bounded request, latency, timeout,
rejection, overload, storage-commit-failure, and client-failure counters.
