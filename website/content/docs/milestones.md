---
title: Current status and milestones
description: Separate implemented behavior from live verification and future work.
---

# Rewrite milestones: what is actually shipped

This page tracks implementation state, not aspiration. A feature being present
in source is not the same as a live Telegram guarantee, so the table keeps
those two claims separate.

## The short version

| Milestone | State | What that means today |
|---|---|---|
| M1 — Inventory and ADR | Complete | The legacy behavior, bounded replacements, and compatibility stance are documented. |
| M2 — Buildable scaffold | Implemented; gate not green | Go, Python, Makefile, pinned tools, config, and a release workflow exist; `make lint` still has staticcheck findings. |
| M3 — Protocol and package v1 | Verified | Versioned DTOs, framed/authenticated RPC, SDK, deterministic `.srnx`, trust checks, rollback, and malformed-input tests pass. |
| M4 — Core vertical slice | Verified | Config, atomic storage, command/alias parsing, watcher filters, bounded queues/workers, backpressure, and rate limiting are covered by tests. |
| M5 — Telegram adapters | Partial | Recorded transport and live adapter code are covered; the real-account smoke test is opt-in and remains unrun by default. |
| M6 — Python sidecar | Verified | Supervision, lifecycle, decorators, config, storage, HTTP/media/jobs bridges, restart behavior, and 34 Python tests pass. |
| M7 — Essential built-ins | Implemented | Loader, help, settings, updater/restart, and diagnostics are part of the core command registry. |
| M8 — Production hardening | Partial | Race tests and boundary tests pass; published benchmark numbers, a clean lint gate, and long soak results are still open. |
| M9 — Compatibility release | Documentation complete; release open | Setup, operations, guides, migration, and matrix exist; no tagged release archive is published yet. |
| M10 — Flagship Autopilot | Implemented; live rehearsal open | Installable bounded Go workflow example, strict schema, durable jobs/state, recovery/chaos tests, and bilingual product docs. |

## Current verification snapshot

Run from the repository root:

| Command | Result in the current checkout |
|---|---|
| `go test ./...` | Pass |
| `go test -race ./...` | Pass |
| `uv run --project sidecar/python python -m unittest discover -s sidecar/python/tests -t sidecar/python` | Pass — 34 tests |
| `make build` | Pass |
| `make fmt-check` | Pass |
| `make lint` | Fails on existing staticcheck findings in the Go tree |
| `make check` | Not green because it includes `make lint` |
| `SERENOX_LIVE_TELEGRAM=1 go test ./internal/telegram -run TestLiveTelegramSmoke` | Explicit operator gate; not part of ordinary CI |

The live smoke gate needs a disposable test account/session, a target peer, and
credentials supplied through one-shot environment variables. It sends,
edits/replies to, and deletes marked messages, then performs best-effort
cleanup. See `internal/telegram/live_smoke_test.go`; no live credentials belong
in the repository.

## What is deliberately still open

- a clean static-analysis gate for the current Go tree;
- published idle/peak RSS, allocation, throughput, and soak numbers;
- a real-account Telegram smoke run in a controlled environment;
- broader inline and client operations beyond the typed bounded subset;
- a compatibility fixture for every Hikka behavior before making a full
  compatibility claim;
- a tagged release with reproducible archives and checksums.

## Required contributor gate

The intended release gate remains:

```text
make fmt
make fmt-check
make lint
make test
make test-race
make build
make legacy-check
```

`make check` runs the same sequence as one command. The legacy `hikka/` tree
must retain its pre-change hash throughout the rewrite.
# Event Fabric status

The local single-daemon protocol, signed policy, durable file-store routing, executor delivery, retry/DLQ/replay primitives, recovery, core Telegram catalog, and Go/Python SDK surfaces are implemented. Public operator CLI/control commands, full core lifecycle/job/control catalog, packaged cross-process fixtures, and dedicated metrics export remain follow-up work. Multi-node guarantees are not planned for protocol v1.
