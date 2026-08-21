# Serenox Python sidecar

This directory contains the optional, process-isolated compatibility host and
the small `serenox_sdk` package used by Python plugins. The host never embeds
Python in the Go daemon. It connects to a daemon-owned local socket and exchanges
versioned, length-prefixed JSON frames.

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
write-oriented client proxy. Reads such as `client.get_me` require duplex
`host.client` RPC, which the current core does not negotiate; the production
host therefore raises an actionable `UnsupportedAPIError` rather than returning
fabricated Telegram data. Synchronous `Module.get/set` use the bounded local
snapshot/cache and emit a durable operation action only while handling a
lifecycle or dispatch request.

```sh
UV_CACHE_DIR=/tmp/serenox-uv-cache uv sync --locked --group dev
uv run ruff format --check .
uv run ruff check .
uv run pyrefly check src tests
uv run python -m compileall -q src tests
uv run python -m unittest discover -s tests -t .
```
