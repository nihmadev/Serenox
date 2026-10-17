---
title: Outbound HTTP service
description: Call allowlisted HTTP origins through the bounded host proxy.
---

# Outbound HTTP client

Serenox plugins do not receive a Go `http.Client`, Python HTTP library, socket,
proxy configuration, or filesystem upload path. A running plugin may request
approved external HTTP(S) resources through the authenticated duplex reverse
RPC method `host.http`.

The v1 DTOs are `HTTPRequest` and `HTTPResponse` in
`pkg/pluginproto/v1`. Requests carry an uppercase method, URL, bounded header
pairs, bounded body, optional response limit, and optional timeout. Responses
carry status, redacted response headers, query-free final URL, bounded body,
redirect count, elapsed milliseconds, content type, and explicit rejected or
truncated state.

Packages must declare capability `http`, permission
`network.http.read` for GET/HEAD, and `network.http.write` for POST/PUT/PATCH/
DELETE. A package must also sign an explicit origin allowlist in `network`.
Empty allowlists deny all network access. Origins match exact host and port by
default; `subdomains` is explicit. Redirects are revalidated against scheme,
port, origin, allowlist, and address policy. Cleartext HTTP requires both
`allow_http` and `network.http.cleartext`.

The core disables ambient proxy variables, verifies HTTPS certificates and
hostnames, resolves names itself, rejects private/link-local/multicast/
unspecified/reserved/metadata addresses, and dials only validated addresses.
Request and response bytes, headers, redirects, timeout, concurrency, rate,
queue, and optional total-byte budgets are finite. Automatic retries are not
performed, especially for writes. Compressed response bodies, cookies,
credential forwarding, binary downloads, webhooks, and inbound listeners are
unsupported by this JSON API. Large binary transfers use the existing bounded
media API.

The Go SDK exposes `HTTPClientFromContext(ctx)` and typed `HTTPRequest`; its
response wrapper provides selected headers and bounded `JSON`. The Python SDK
exposes `await self.http.request(...)` and `response.json(max_bytes=...)`.
Both APIs fail after disconnect or outside a running dispatch lifecycle.

The weather fixture in `examples/plugins/weather` uses only read permission,
the approved `api.open-meteo.com` origin, a 64 KiB response limit, no redirects,
and a bounded per-chat cooldown. It is a deterministic-shape example; normal
tests do not call a public weather service.
