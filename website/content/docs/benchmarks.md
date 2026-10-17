---
title: Benchmarks and resource gates
description: Measure dispatch throughput, resource usage, and shutdown behavior.
---

# Benchmarks and resource gates

The benchmark suite reports:

- command parse/filter/dispatch latency;
- accepted and overloaded throughput;
- allocations per update;
- idle and peak resident set size;
- queue depth, handler timeout, and limiter eviction counts;
- goroutine/process/socket counts before and after shutdown.

Run CPU/allocation benchmarks with:

```sh
go test -run '^$' -bench . -benchmem ./...
```

Long-running resource tests are opt-in to keep ordinary contributions fast:

```sh
SERENOX_SOAK=1 go test -run 'Soak|Resource' -count=1 ./...
```

Verified numbers are written here only from a completed run with Go version,
OS/architecture, CPU, duration, fixture, queue/worker settings, idle RSS, peak
RSS, and final RSS. No production resource target is claimed before that run.
