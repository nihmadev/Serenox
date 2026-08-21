# Legacy compatibility matrix

Status meanings:

- **verified** — automated fixture or golden test passes;
- **partial** — a documented subset passes;
- **planned** — contract exists but behavior has not passed a compatibility fixture;
- **unsupported** — fails explicitly with migration guidance.

| Surface | Status | Notes/evidence |
|---|---|---|
| One- or multi-character owner command prefix | verified | `TestParserLegacyEligibility` covers case folding, arguments, self ID, media exclusions, and a two-character prefix. |
| Case-insensitive commands and aliases | verified | `TestCommandRegistryAliasPrecedenceAndCoreProtection` and built-in persistence tests pass. |
| Watcher direction/chat/media/text filters | verified | `TestCompiledWatcherAllFilters`, route-before-enqueue tests, and Python `test_watcher_metadata_and_filter_defense` pass. |
| Per-plugin overload isolation | verified | Go executor overload/fixed-worker tests and Python `test_queue_overload_is_bounded_and_retryable` pass. |
| Core namespaced JSON storage | verified | Atomic file, failed-batch rollback, bounds, concurrency, and size/cardinality fixtures pass. |
| Python plugin durable storage bridge | planned | The sidecar validates a bounded local snapshot and rollback behavior, but duplex `host.storage` RPC is reserved rather than wired to the Go store. |
| Module lifecycle hooks | verified | Go dependency/rollback lifecycle tests and Python authenticated lifecycle/disconnect-unload fixtures pass. |
| `Module`, command/watcher decorators | verified | Python `test_decorators_publish_core_filters_and_owner_metadata` and filter-defense fixtures pass for the documented subset. |
| `ModuleConfig`, `ConfigValue` | verified | Python typed configuration and validation fixtures pass for JSON-compatible values. |
| Python `Module.get`/`set` | partial | Bounded cache and enqueue rollback fixtures pass; persistence across sidecar restart awaits the `host.storage` bridge. |
| Reply/edit/delete actions | verified | Protocol validation, permission checks, sidecar lowering, and recorded transport action fixtures pass. |
| Inline/client actions | partial | DTOs, bounds, permission checks, and sidecar lowering pass; a live Telegram adapter is not yet verified. |
| Forms/lists/galleries | partial | Bounded unit model exists; live Bot API adapter is not yet verified. |
| Raw Telegram update classes | unsupported | Add an explicit protocol DTO for each required update. |
| Arbitrary Pyrogram/Aiogram client API | unsupported | Use a documented client operation or request an explicit DTO. |
| `import_lib` and runtime `pip` installation | unsupported | Package dependencies into the sidecar/plugin artifact. |
| In-process Python evaluation | unsupported | External process plugins only. |
| Unrestricted terminal/apt module | unsupported | Deliberate non-goal of the compatibility core. |
| Full Hikka compatibility | unsupported | No such claim until every published row is fixture-backed. |

Known fixed incompatibilities include the legacy multi-character prefix slicing
bug, ineffective chat rate counter, per-event decay timers, detached watcher
tasks, retained zero counters, synchronous non-atomic database writes, and
unbounded inline callback/media state.
