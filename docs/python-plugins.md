# Python compatibility plugins

Python compatibility runs in a supervised child process; CPython is not linked
into the Go binary. The stable package is `serenox_sdk`.

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

The current sidecar records `Module.get`/`set` updates in its bounded local
snapshot and emits typed operations from active handlers. Durable storage across
a sidecar restart remains disabled until the core negotiates duplex
`host.storage` RPC; plugins must not treat the local cache as durable state.

See [`compatibility.md`](compatibility.md) for fixture-backed status. A module
must be loaded in the sidecar compatibility suite before it is described as
compatible.
