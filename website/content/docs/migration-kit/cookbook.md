---
title: Migration rewrite cookbook
description: Rewrite common legacy patterns with verified Serenox building blocks.
---

# Migration rewrite cookbook

The safest migration is a sequence of small rewrites. First preserve the user
visible behavior with a bounded command or watcher. Then add state, network,
media, or moderation only when the smaller version is tested.

## 1. Start from the legacy behavior, not the legacy imports

Write down:

- the event that starts the behavior;
- who may trigger it;
- which chat or message it may affect;
- whether it reads or mutates Telegram state;
- what happens when the operation is slow, rejected, or uncertain;
- the smallest persistent state it needs.

Delete imports such as `pyrogram`, `telethon`, `aiogram`, `requests`, and
`aiohttp` from the module. Serenox plugins use DTOs and host services so the
core can enforce permissions, limits, and rollback.

## 2. Rewrite a command

Legacy shape:

```python
@loader.command()
async def uwucmd(self, message):
    await message.edit("UwU, nya")
```

Serenox shape:

```python
from serenox_sdk import Module, loader
from serenox_sdk.actions import Message


class UwU(Module):
    @loader.owner
    @loader.command(name="uwu", aliases=("nya",))
    async def uwu(self, message: Message) -> None:
        await message.reply("UwU, nya~")
```

Use `reply` when the command should leave a new message. Use `edit` only when
the command is intentionally changing the owner's outgoing command message.
Declare the corresponding permission; never rely on a hidden client call.

## 3. Rewrite a watcher

Legacy watcher logic often checks every message inside the process. Move the
cheap, deterministic part into the decorator so the core can reject unrelated
updates before the plugin queue:

```python
@loader.watcher(contains="uwu", incoming=True, no_commands=True)
async def uwu_watcher(self, message: Message) -> None:
    await self._maybe_reply(message)
```

Keep a second check in the handler for safety-sensitive behavior. A watcher
must tolerate being dropped under overload and must not assume at-least-once
delivery.

## 4. Add a bounded cooldown

Do not create one timer per event or retain an unbounded chat map:

```python
now = time.monotonic()
previous = self._last_reply.get(message.chat_id, 0.0)
if now - previous < self.config["cooldown_seconds"]:
    return
if len(self._last_reply) >= 256 and message.chat_id not in self._last_reply:
    self._last_reply.pop(next(iter(self._last_reply)))
self._last_reply[message.chat_id] = now
await message.reply(self.config["reply"])
```

Prefer a monotonic clock, a fixed maximum, and a visible cooldown setting.

## 5. Add typed configuration

Legacy `ModuleConfig` values that accept anything should become explicit:

```python
from serenox_sdk import ConfigValue, ModuleConfig, validators

config = ModuleConfig(
    ConfigValue("enabled", True, "Enable the behavior", validator=validators.Boolean()),
    ConfigValue(
        "cooldown_seconds", 20, "Per-chat cooldown",
        validator=validators.Integer(minimum=1, maximum=3600),
    ),
)
```

Reject unknown options and keep secrets out of replies and logs. Configuration
validation is part of the module contract, not a UI-only convenience.

## 6. Migrate storage

Replace pointer-style or whole-database mutations with a small namespaced JSON
value:

```python
count = await self.async_get("count", 0)
await self.async_set("count", int(count) + 1)
```

State must be JSON-serializable and bounded. Treat `OverloadedError` and commit
failures as real outcomes. Do not store Telegram client objects, open files,
unbounded message histories, or opaque library objects.

## 7. Migrate an HTTP integration

Use the host client, not a language-level socket library:

```python
response = await self.http.request(
    "GET",
    "https://api.example.com/translate",
    headers={"accept": "application/json"},
    timeout=10,
    max_response_bytes=64 * 1024,
)
payload = response.json(max_bytes=64 * 1024)
```

The manifest must declare `http`, the correct network permission, and an exact
origin allowlist. Keep user text out of URLs when a provider supports a body;
never put credentials in query strings or logs. A failed or timed-out write is
not silently retried.

## 8. Migrate media

Media references are opaque handles. The plugin may inspect, download, upload,
attach, and delete only through the bounded media client declared by the
manifest. It must not accept a user-provided filesystem path or construct a
second Telegram connection.

The `examples/plugins/convert` fixture is the reference pattern: inspect the
input, stream it to a private temporary file, transform it with bounded input,
upload the result, and return an explicit attachment action.

## 9. Migrate moderation

Start with a narrow, auditable policy:

```python
@loader.watcher(incoming=True, only_groups=True, no_commands=True)
async def moderate(self, message: Message) -> None:
    if not self._matches_policy(message):
        return
    await message.delete()
```

Add explicit chat IDs, owner configuration, a dry-run mode, and a bounded audit
reply before enabling destructive actions. If the required member-management
operation has no typed Serenox DTO, the correct migration result is
`unsupported`, not a raw client escape hatch.

## 10. Migrate reminders

Do not copy `@loader.loop` or create an unmanaged task. A reminder needs a
durable schedule model, an owner, a bounded number of entries, cancellation,
restart semantics, and an explicit scheduler capability. Until that capability
is available, implement a command-driven reminder or keep the behavior out of
the migrated plugin and say why.

## 11. Run the migration kit

```sh
serenox plugin preflight --source ./my-plugin
make python-sync
python -m unittest discover -s sidecar/python/tests -t sidecar/python
serenox plugin pack --source ./my-plugin --output ./dist/my-plugin.srnx
serenox plugin verify ./dist/my-plugin.srnx
```

Warnings require review. Errors must be fixed before packaging. A clean
preflight does not grant compatibility with an API it cannot see; add a typed
fixture or document the refusal when the behavior is important.
