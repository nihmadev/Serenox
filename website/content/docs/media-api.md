---
title: Media API
description: Transfer and attach media through opaque handles and bounded streams.
---

# First-class media API

Protocol `serenox.plugin/1` exposes media as metadata plus an authenticated,
bounded transfer channel. The daemon's supervised Go/Python bridge currently
uses the same framed RPC connection and carries one 64 KiB chunk per request;
the optional native binary data plane uses the `SRXM` framing described below.
Chunks are bounded and validated before they reach a plugin or the daemon
store.

`Media` contains an opaque handle, kind, MIME type, filename, declared size,
SHA-256 checksum, and source ownership. `host.media` supports `inspect`,
`download`, `upload`, `attach`, and `delete`. Uploads finalize only after the
declared byte count and checksum match. Handles are checked against plugin,
account, and optional peer ownership before every operation.

The binary stream uses a versioned `SRXM` header, request ID, transfer ID,
monotonic sequence, declared size, HMAC-SHA256 tag, and final checksum record.
Duplicate, stale, out-of-order, oversized, unauthenticated, truncated, and
incomplete transfers fail closed.

v1 limits are 64 MiB per object, 64 KiB per chunk, five minutes maximum
duration, 30 seconds idle timeout, two concurrent transfers per plugin, and a
256 MiB retained quota per plugin. Packages may request lower values only.

Permissions are `media.read` for inspect/download, `media.write` for
upload/delete, and `media.attach` for reply/send/edit actions with a handle.
Capabilities are `media_read`, `media_write`, and `media_attach`. There are no
byte-string, arbitrary-path, URL, or implicit-network APIs.

Supported binary kinds are `photo`, `video`, `audio`, `voice`, `document`,
`sticker`, and `animation`. Structured Telegram locations, contacts, polls,
dice, and unknown media return `media_unsupported` for binary operations.

Uploaded data lives in a daemon-owned mode-0700 temporary store and is renamed
atomically on finalization. Abandoned data is removed on cancellation, timeout,
disconnect, crash/restart, uninstall, and daemon shutdown. Format conversion
must happen in the plugin using bounded streaming and a pinned, declared
converter; no runtime package installation or network fetch is allowed.
