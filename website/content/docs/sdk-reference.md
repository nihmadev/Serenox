---
title: Go and Python SDK reference
description: Maintained plugin SDK surfaces and runtime differences.
---

# Go and Python SDK reference

Authoritative wire types live in `pkg/pluginproto/v1`. The Go SDK is in `sdk/go`; Python package `serenox_sdk` is under `sidecar/python/src`. Both expose actions, storage, HTTP, media, jobs, events, client reads, moderation, and lifecycle integration where the matrices mark support.

## Go SDK

`sdk/go.Server` registers metadata and serves the authenticated protocol. Typed clients cover host services. Handlers receive protocol DTOs and a cancellable context. See [Go plugins](go-plugins.md) and `sdk/go/server_test.go`.

## Python SDK

Subclass `serenox_sdk.Module` and use `serenox_sdk.loader` decorators. Host services are async and return typed results or structured errors. The sidecar validates frames, lifecycle order, batches, deadlines, and metadata. See [Python plugins](python-plugins.md) and `sidecar/python/tests`.

| Concern | Go | Python |
|---|---|---|
| Entrypoint | platform executable | module and class in managed environment |
| Concurrency | contexts and SDK dispatch | async sidecar handlers |
| Dependencies | compiled into executable | reproducibly packaged; no runtime `pip` |
| Shared contract | protocol v1 DTOs and permissions | protocol v1 DTOs and permissions |

An exported DTO does not prove live Telegram support. Check the [client](telegram-client-matrix.md) and [moderation](moderation-implementation-matrix.md) matrices.
