---
title: Migration behavior matrix
description: Map legacy module behaviors to explicit Serenox APIs and boundaries.
---

# Migration behavior matrix

This matrix maps common Hikka/Heroku behaviors to stable Serenox building
blocks. “Verified” means the replacement is covered by the current protocol,
runtime, or a repository fixture. It does not mean that every legacy module
using the behavior can be copied unchanged.

| Legacy behavior | Serenox analogue | Required declaration | Boundary and migration note | Reference |
|---|---|---|---|---|
| Owner command (`@loader.command`, `foo()`/`foocmd`) | Python `@loader.owner` + `@loader.command` | `commands`, usually `telegram.reply` | Arguments are parsed from the bounded message DTO; core owns owner checks | [`uwu/plugin.py`](../../../../examples/plugins/uwu/plugin.py) |
| Command aliases | `aliases=(...)` in command metadata | `commands` | Aliases are declared during handshake and cannot replace core commands | [`plugin-development.md`](../plugin-development.md) |
| Message watcher | `@loader.watcher(...)` | `watchers`, plus action permission | Filters run before plugin queue admission; overload drops watcher work explicitly | [`uwu/plugin.py`](../../../../examples/plugins/uwu/plugin.py) |
| Keyword auto-reply | Filtered watcher + bounded cooldown | `watchers`, `telegram.reply` | Keep cooldown state bounded and do not answer outgoing messages by default | [`uwu/plugin.py`](../../../../examples/plugins/uwu/plugin.py) |
| Auto-moderation: delete a message | `await message.delete()` | `watchers`, `telegram.delete` | Start with explicit chat/sender/content filters; no arbitrary raw update access | [`actions.go`](../../../../pkg/pluginproto/v1/actions.go) |
| Auto-moderation: warn or mute workflow | reply/edit/delete actions plus a typed client operation when available | explicit action/client permissions | Moderation policy should be narrow and auditable; unsupported member mutations must fail closed | [`compatibility.md`](../compatibility.md) |
| Translation through a provider | `await self.http.request(...)` | `http`, `network.http.read` or `network.http.write`, signed origin allowlist | Cap request/response bytes, timeout, rate, and origins; no ambient sockets or cookies | [`weather`](../../../../examples/plugins/weather) |
| Media download/transform/upload | `MediaClient` with opaque handles and bounded streams | `media_read`, `media_write`, `media_attach` | Never accept filesystem paths or load an unbounded file into memory | [`convert`](../../../../examples/plugins/convert) |
| Reminder stored in a loop | namespaced storage plus the typed durable Jobs API | `storage`, `jobs`, and `jobs.schedule` | Use one-shot/fixed-delay/fixed-rate/calendar jobs; do not port `@loader.loop` as an unmanaged task. See [`jobs-api.md`](../jobs-api.md) | [`jobs-api.md`](../jobs-api.md) |
| HTTP integration | host HTTP reverse RPC | `http` + network permission + non-empty origin allowlist | HTTPS, exact origins, finite bytes/time/concurrency; no inbound listener | [`http-client.md`](../http-client.md) |
| Link preview/shortener/parser | pure parsing or host HTTP lookup | `telegram.reply` and optionally `http` | Parse URLs with a bounded standard-library helper; do not fetch arbitrary user URLs without an allowlist | [`cookbook.md`](cookbook.md) |
| Safe admin automation | owner-only command, explicit chat IDs, typed actions | least-privilege Telegram permissions | Avoid “admin everywhere”; validate target chat/message IDs and make destructive actions opt-in | [`cookbook.md`](cookbook.md) |
| Persistent module state | `async_get`, `async_set`, `async_delete` | `storage`, `storage.read`/`storage.write` | Values are JSON and bounded; handle commit failure and overload | [`storage.go`](../../../../pkg/pluginproto/v1/storage.go) |
| Inline forms and callbacks | bounded inline/callback DTOs and opaque callback IDs | `inline`, `telegram.inline` | Callback state has a TTL and does not survive a sidecar restart | [`compatibility.md`](../compatibility.md) |
| `client.send_message` / `get_messages` | documented `ClientProxy` operation | `client_operations` plus exact `client.<operation>` permission | Only operations with a versioned DTO and adapter fixture are available | [`client.go`](../../../../pkg/pluginproto/v1/client.go) |
| Raw Pyrogram/Telethon/Aiogram object | no direct equivalent | none | Replace with a DTO or accept an explicit unsupported result; do not add a generic raw invoke escape hatch | [`compatibility.md`](../compatibility.md) |
| `import_lib`, runtime `pip`, remote module loading | packaged dependency metadata | none beyond package contract | Dependencies must be shipped and verified; startup never downloads or installs code | [`plugin-packages.md`](../plugin-packages.md) |
| `eval`, terminal, shell, `os.system` | no equivalent | none | Refuse the behavior; use a typed host service or a separate operator tool | [`compatibility.md`](../compatibility.md) |

## Permission review rule

Use the smallest declaration that explains the behavior:

- reply only: `telegram.reply`;
- delete only: `telegram.delete`;
- edit only: `telegram.edit`;
- send to another chat: `telegram.send`;
- read media: `media.read`;
- upload or delete plugin-owned media: `media.write`;
- attach media to Telegram: `media.attach`;
- read-only HTTP: `network.http.read`;
- HTTP writes: `network.http.write`;
- durable state: `storage.read` and/or `storage.write`.

The package verifier and runtime enforce declarations, but a human should still
review whether the behavior deserves the permission. A declaration that is
technically valid can still be too broad for the plugin's purpose.
