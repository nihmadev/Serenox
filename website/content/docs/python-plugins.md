---
title: Python plugin development
description: Build, test, package, and run a plugin in the managed Python sidecar.
---

# Python plugins

> New to Serenox plugin development? Start with the
> [complete author guide](plugin-development.md), then use this page as the
> Python compatibility reference.

Python is a supported first-class runtime in a supervised child process;
CPython is not linked into the Go binary. The stable package is
`serenox_sdk` (SDK `0.1.0`) and the host implements protocol `1.0`/sidecar
`0.1.0`.

The supported interpreter is Python 3.10 or newer. Serenox discovers the
configured executable, managed runtime, bundled development environment, or
`python3`, then probes all versions before install/activation. A package must
declare `sdk` and `sidecar` ranges and an entrypoint `module` plus `class`.

```python
from serenox_sdk import Module, command, watcher


class Example(Module):
    strings = {"name": "Example"}

    @command(alias="p")
    async def ping(self, message):
        await message.edit("pong")

    @watcher(contains="hello", incoming=True)
    async def greet(self, message):
        await message.reply("hello")
```

Decorators are inspected at load time and converted into protocol filter DTOs,
so the Go core rejects non-matching updates before sidecar admission.

## Supported model

- lifecycle hooks (`config_complete`, `client_ready`, `on_unload`);
- commands, watchers, inline/callback declarations and common filter tags;
- typed configuration and a bounded per-process JSON storage cache;
- reply, edit, delete, send, inline actions, and the finite documented client
  operation set.

Direct Pyrogram/Aiogram objects, raw update classes, runtime package
installation, remote Python library loading, and arbitrary client attributes are
outside the finite SDK. Access raises an actionable `UnsupportedAPIError`
instead of silently returning a partial object.

`async_get`, `async_set`, and `async_delete` use the authenticated duplex
`host.storage` RPC and return only after the core commits the atomic store
update. The older synchronous `Module.get`/`set` helpers remain bounded cache
compatibility APIs; they are not a substitute for the async durability path.

See [`compatibility.md`](compatibility.md) for fixture-backed status. A module
must be loaded in the sidecar compatibility suite before it is described as
compatible.

Runtime policy is intentionally finite: the daemon supplies an isolated
allowlisted environment and verified package directory, bounds queues, workers,
action batches, storage, callbacks, frames, and handler/shutdown timeouts, and
never installs dependencies during startup. Unsupported APIs raise an
`UnsupportedAPIError` naming the API, reason, and supported replacement.

The read client is exposed as typed groups on `self.client`: `identity`,
`peers`, `users`, `chats`, `messages`, `history`, `search`, `dialogs`,
`participants`, and `media_metadata`. For example, an archive plugin can use
`page = await self.client.history.list(chat_id, limit=50)` and persist only
`page.cursor` in its own storage. The cursor is opaque, expiring, and scoped to
the plugin/account/query; no Telegram library import or hidden iterator is
required.

Media uses the typed async `MediaClient` and bounded binary file-like streams;
bytes, paths, URLs, and implicit network access are rejected. See
[media-api.md](media-api.md).

For approved outbound APIs, use `await self.http.request(...)` and
`response.json(max_bytes=...)`. This is the typed `host.http` reverse-RPC
surface; it is unavailable before `client_ready`, after disconnect, without
the verified `http` capability, or outside the package's signed origin policy.
See [http-client.md](http-client.md) and the weather fixture in
`examples/plugins/weather`.

## Typed moderation

Bound modules expose `self.moderation.members`, `.admins`, `.messages`,
`.settings`, `.join_requests`, `.invites`, and `.topics`. A `Message` has the
read-only `reply_to_message_id`, `get_reply_message()`, and
`moderation_reply_target()` helpers, so reply moderation does not require
reaching into `message.dto`. See [moderation-api.md](moderation-api.md).
# Event Fabric

Declare `EventPublication` and `EventSubscription` on the module, decorate stable handlers with `@event_handler`, and publish with `await self.events.publish(...)`. Return `event_success`, `event_retry`, `event_reject`, or `event_ambiguous`. The managed sidecar event loop owns reverse-RPC cancellation and pending-map cleanup; do not detach handler tasks.
