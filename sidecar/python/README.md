# Serenox Python sidecar

This directory contains the process-isolated Python runtime host and
the small `serenox_sdk` package used by Python plugins. The host never embeds
Python in the Go daemon. It connects to a daemon-owned local socket and exchanges
versioned, length-prefixed JSON frames.

The supported contract is Python 3.10+, sidecar `0.1.0`, SDK `0.1.0`, and
protocol `1.0`. Packages declare these ranges plus an explicit local module and
class. Dependencies are supplied as verified package files/lock metadata; this
runtime never runs `pip` or downloads code during startup.

The wire contract is exactly `serenox.plugin/1` (`1.0`) from
`pkg/pluginproto/v1`: request/response envelopes carry explicit deadlines,
the authenticated handshake echoes a nonce, and Telegram-library objects never
cross the socket. Dispatch admission, response queues, workers, callback
registries, action batches, action sizes, frames, and the compatibility storage
cache all have fixed configurable bounds.

The compatibility surface is intentionally finite. Missing legacy attributes
raise `UnsupportedAPIError` with a replacement hint rather than failing later
with an ambiguous `AttributeError`.

Supported compatibility calls include module lifecycle hooks, command/watcher/
inline/callback decorators, typed `ModuleConfig`, namespaced JSON storage,
`Message.reply/edit/delete`, bounded inline forms and answers, and the finite
client proxy. `async_get`, `async_set`, and `async_delete` use typed duplex
`host.storage` requests and return only after the Go store commits its atomic
file replacement. `client.get_me`, `client.get_message`, and
`client.get_entity` use typed duplex `host.client` requests and return
transport-neutral DTOs. Connections that do not negotiate `duplex_host_rpc`
raise an actionable `UnsupportedAPIError`; they never fabricate values.

```sh
UV_CACHE_DIR=/tmp/serenox-uv-cache uv sync --locked --group dev
uv run ruff format --check .
uv run ruff check .
uv run pyrefly check src tests
uv run python -m compileall -q src tests
uv run python -m unittest discover -s tests -t .
```
