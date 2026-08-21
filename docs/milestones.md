# Rewrite milestones

Each checked item requires the commands named in the gate to pass from
the repository root. The legacy `hikka/` tree must retain its pre-milestone hash.

Only M1 is marked complete. Recent `.srnx`, process-supervision, runtime,
sidecar, and Telegram changes have source audits or targeted evidence, but the
complete `make check`, build, race, and CI gates have not been rerun on the
combined tree. A component being present below therefore does not close its
milestone gate.

- [x] **M1 — Inventory and ADR:** inspect the requested Python sources; record
  behavior, bounded replacements, compatibility stance, and migration order.
  **Gate:** both docs exist; all cited paths resolve; legacy tree hash unchanged.
- [ ] **M2 — Buildable scaffold:** Go module, package skeleton, Python sidecar
  skeleton, pinned tools, local configs, pre-commit, Makefile, and CI.
  **Gate:** `make fmt-check lint test build` and a clean legacy hash.
- [ ] **M3 — Protocol/lifecycle and package v1:** versioned DTOs, framed RPC,
  handshake, transport abstraction, registry state machine, Go SDK,
  deterministic `.srnx` schema/CLI, trust verification, atomic version
  management, and malformed-input tests. **Gate:** protocol and package
  golden/security/lifecycle tests plus `go test -race ./...`.
- [ ] **M4 — Core vertical slice:** typed config, transactional storage, command
  parsing/aliases, watcher filters, per-plugin bounded queues/workers, backpressure,
  and timer-free bounded rate limiting.
  **Gate:** unit and overload tests prove configured caps; `make check`.
- [ ] **M5 — Telegram/inline adapters:** transport interface, recorded update
  replay, native live session connectivity, action mapping, bounded inline
  units/callbacks, and disconnect recovery. **Gate:** recorded golden cases,
  live-adapter boundary tests, adapter integration tests, and `make check`.
- [ ] **M6 — Python sidecar:** supervised process/socket cleanup, SDK lifecycle,
  decorators, config/storage, message/inline/client proxies, explicit unsupported
  API errors, restart fixtures.
  **Gate:** Ruff, type checker, `compileall`, compatibility fixtures, crash tests.
- [ ] **M7 — Essential built-ins:** loader/plugin management, help, settings and
  config, updater/restart, info/diagnostics, and required inline glue.
  **Gate:** selected legacy golden tests and end-to-end command tests pass.
- [ ] **M8 — Production hardening:** malformed RPC, timeout, panic/crash,
  restart/circuit breaker, shutdown/leak, race, load, soak, latency, allocation,
  idle/peak RSS, and subprocess/socket cleanup tests.
  **Gate:** `make check`; published benchmark results; no leaked resources.
- [ ] **M9 — Compatibility release:** setup/operations, architecture,
  contributing, Go/Python plugin guides, migration/import guide, supported API
  matrix, and known incompatibilities.
  **Gate:** clean install rehearsal, reproducible builds, all CI jobs green.

Required commands from M2 onward:

```text
make fmt
make fmt-check
make lint
make test
make test-race
make build
make check
```

Current transport note: recorded NDJSON replay and transport interfaces exist;
the source now also wires `serenox login` to gotd phone/code/optional-2FA,
persists the session atomically, and gives the daemon a noninteractive live
branch which verifies the saved account identity. The gotd source/API audit
passed. Local gotd compilation and a real-account network fixture were stopped
by the memory/OOM guard. Plain/HTML send/reply/edit/delete mapping and bounded
peer resolution are present in source, while buttons, inline/client actions,
the remaining adapter fixtures, and full gates are incomplete. M5 therefore
remains unchecked.

Current targeted `.srnx` evidence: the compiling echo example passed
`go test ./examples/plugins/echo`, and a real build → package → verify cycle
completed successfully through the shared package engine. The canonical
user-facing prefix is now `serenox plugin`; its subcommands are `pack`, `verify`,
`install`, `list`, `rollback`, and `uninstall`. `srnx-plugin` and
`serenox-plugin` are compatibility aliases. This evidence covers the
example/package path only and is not a substitute for any unchecked milestone
gate above.
