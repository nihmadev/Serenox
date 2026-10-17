---
title: Architecture and isolation model
description: Understand the bounded Go core, external plugins, and protocol boundaries.
---

# Serenox Go architecture decision record

**Status:** Accepted for the staged rewrite
**Scope:** Go code at the repository root; the `hikka/` Python tree remains a runnable reference.
**Legacy snapshot inspected:** `hikka/main.py`, `client.py`, `dispatcher.py`,
`loader.py`, `types.py`, `database.py`, every file in `hikka/inline/`, and every
file in `hikka/modules/`.

## 1. Observed legacy behavior

### Bootstrap and Telegram transport

- `Hikka._main` creates a Pyrogram client with HTML parsing and then enters it;
  `amain_wrapper` resolves the current user before `amain` initializes the
  database, translations, module registry, dispatcher, modules, and inline bot
  (`hikka/main.py:330-345`, `428-480`).
- The startup order is observable and must be retained: storage -> translations
  -> registry -> dispatcher -> core/external registration -> config hooks ->
  ready hooks -> startup notification (`hikka/main.py:428-452`).
- Pyrogram receives ordinary and edited messages. Watchers run from group 0;
  owner commands run from group 1. Forwarded or non-owner messages are excluded
  from command handling (`hikka/main.py:378-420`). Raw update registration is
  currently commented out (`hikka/main.py:422-426`).
- `HikkaClient` is a thin Pyrogram subclass carrying the current user, database,
  dispatcher, module registry, inline manager, translator, and a channel cache
  (`hikka/client.py:18-52`). These library types must not cross the new plugin
  boundary.
- Legacy startup reads `api_id`/`api_hash` from its root configuration or
  environment and builds a named Pyrogram session with generated device name,
  fixed `Windows 10` system metadata, version metadata, and English locale
  (`hikka/main.py:300-328`, `457-471`). Phone and QR choices are startup inputs,
  not plugin configuration (`hikka/main.py:184-198`).

### Commands, watchers, and rate limiting

- The command prefix is stored at owner `hikka.main`, key `command_prefix`, and
  defaults to `.`. A command is text from the current account, is not just the
  prefix, and is rejected for sticker/dice/audio/via-bot messages
  (`hikka/dispatcher.py:178-211`). `Modules.dispatch` resolves the exact command,
  persisted aliases, and decorator aliases in that order
  (`hikka/loader.py:890-934`).
- Commands in internal `hikka-*` supergroups other than `hikka-logs` are ignored,
  and edited channel posts may be ownership-checked through the event log
  (`hikka/dispatcher.py:213-240`). Command failures are rendered back into the
  message, with special RPC/FloodWait text and optional inline traceback
  (`hikka/dispatcher.py:269-312`).
- Watchers are filtered by global chat blacklist/whitelist, per-module chat
  lists, module whitelist, disabled-watcher modes, and handler tags before they
  run (`hikka/dispatcher.py:429-502`). Supported tag behavior includes direction,
  chat kind, media kind, forward/reply/mention state, text prefix/suffix/contains,
  regex, sender/chat ID, arbitrary filter, and command/non-command state
  (`hikka/dispatcher.py:49-92`, `317-427`).
- Legacy dispatch launches one detached `asyncio.ensure_future` per accepted
  command/watcher (`hikka/dispatcher.py:253-267`, `494-502`). Rate-limit decay
  creates one `call_later` timer per event and retains user/chat maps indefinitely
  (`hikka/dispatcher.py:95-101`, `136-176`). These implementation details are not
  compatible with the bounded-resource objective and will not be copied.

### Module discovery and lifecycle

- Decorators attach metadata for commands, inline handlers, callbacks, watchers,
  raw updates, strict rate limiting, aliases, and filters
  (`hikka/loader.py:328-489`). Legacy suffix discovery is also supported:
  `*cmd`, `_inline_handler`, `_callback_handler`, and the exact method name
  `watcher` (`hikka/types.py:946-992`).
- Core files and account-specific `loaded_modules/*_<tg_id>.py` files are loaded
  dynamically; secure boot skips external files (`hikka/loader.py:550-624`).
  Registration instantiates the first `Module` subclass, prevents external code
  from replacing core modules/commands, and registers commands, inline handlers,
  callbacks, watchers, raw handlers, and loops (`hikka/loader.py:626-733`,
  `854-888`, `1004-1065`).
- Lifecycle order is `internal_init`, config population, `config_complete`,
  optional `on_dlmod`, `client_ready`, and eventually `on_unload`. `SelfUnload`
  removes a module; `SelfSuspend` leaves it installed but without handlers
  (`hikka/types.py:129-164`, `hikka/loader.py:936-1002`, `1004-1058`).
- `@loader.loop` supports interval, autostart, wait-before, persistent stop
  clause, cancellation, and `StopLoop` (`hikka/loader.py:145-255`). The registry
  also starts an unowned perpetual 30-second handler rebuild task
  (`hikka/loader.py:495-548`); Go will instead own and stop all workers.
- The base module injects client, storage, lookup, prefix, inline, and account
  identifiers, and exposes command invocation, animation, namespaced get/set,
  pointer-like collection storage, join requests, and remote Python library
  imports (`hikka/types.py:120-201`, `275-471`, `473-666`).
- `ModuleConfig` accepts legacy triples or typed `ConfigValue` entries; values
  have defaults, docs, validators, change hooks, and a save marker
  (`hikka/types.py:776-848`, `859-943`). Legacy security decorators are currently
  no-ops (`hikka/loader.py:103-126`); this is an observed fact, not a security
  contract to reproduce.

### Persistence

- Account state is an owner/key JSON object in `config-<tg_id>.json`. `get` and
  `set` are namespaced, values must be JSON-serializable, and mutable list/dict
  pointers write through to storage (`hikka/database.py:75-105`, `223-310`).
- Each `set` synchronously serializes the whole database. Validation and a small
  in-memory revision list attempt recovery, but writes are not transactional or
  atomic (`hikka/database.py:107-180`). Telegram documents can also be stored in
  and fetched from a private `hikka-assets` channel (`hikka/database.py:182-220`).
- Process bootstrap settings use a separate root `config.json`
  (`hikka/main.py:47-50`, `114-148`). The Go migration may import these files but
  must never rewrite them.

### Inline bot

- `InlineManager` owns a separate Bot API client/dispatcher, token acquisition,
  unit maps, callback maps, FSM state, polling, and a five-second TTL cleaner
  (`hikka/inline/core.py:51-111`, `113-228`). It routes inline queries, callbacks,
  chosen results, and private bot messages (`hikka/inline/events.py:33-358`).
- Inline handlers return article/photo/GIF/video/document descriptions; an empty
  query returns command help (`hikka/inline/events.py:52-230`, `360-476`).
  Callback access is owner/allow-list/security controlled before invocation
  (`hikka/inline/events.py:232-315`).
- Forms, paged text lists, galleries, and query galleries retain unit state,
  optional TTL, callbacks, allow lists, unload hooks, and edit/delete operations
  (`hikka/inline/form.py:60-375`, `hikka/inline/list.py:35-315`,
  `hikka/inline/gallery.py:58-652`, `hikka/inline/query_gallery.py:24-154`,
  `hikka/inline/types.py:29-203`). Markup normalization supports URL, callback,
  input, close/unload, pagination, and switch-inline actions
  (`hikka/inline/utils.py:75-235`, `618-781`).

### Shipped modules

| Legacy module | Observable role and reference |
|---|---|
| Loader | Repository/file install, persisted external list, dependency retry, unload/clear/reload (`hikka/modules/loader.py:188-352`, `355-933`, `952-1140`). |
| Help | Per-module and aggregate command/inline help, aliases, hidden modules, fuzzy module match (`hikka/modules/help.py:52-304`). |
| HikkaSettings | Watcher controls, prefix/aliases, core toggles, restart/update actions, internal diagnostics (`hikka/modules/hikka_settings.py:33-224`, `245-503`, `530-683`). |
| HikkaConfig | Validated interactive and direct module/library configuration (`hikka/modules/hikka_config.py:67-904`, `906-1016`). |
| Updater | Confirmed restart/secure boot, restart-message persistence, source link, restart completion (`hikka/modules/updater.py:40-132`, `177-210`). |
| Tester | Log export/clear, ping/uptime, failure fixture, Telegram log setup (`hikka/modules/test.py:75-285`). |
| HikkaInfo | Version/build/platform/module/resource information and customization (`hikka/modules/hikka_info.py:43-120`). |
| InlineStuff | Inline cleanup/gallery bridge and bot `/start` response (`hikka/modules/inline_stuff.py:22-83`). |
| Evaluator | In-process Python evaluation with selected globals and secret redaction (`hikka/modules/eval.py:29-144`). |
| Terminal | Shell/apt execution, streamed edits, and process termination (`hikka/modules/terminal.py:299-405`). |

## 2. Decision: bounded native core with isolated plugin hosts

### Package boundaries

The implementation uses these ownership boundaries (exact internal filenames may
evolve without changing the contracts):

- `cmd/serenoxd` and `internal/app`: composition, signals, startup, shutdown.
- `internal/telegram`: session and update transport interface plus the selected
  Telegram adapter and recorded-update adapter.
- `internal/dispatch`: command parser/alias registry and watcher filter compiler.
- `internal/executor`: bounded per-plugin queues, fixed workers, overload policy.
- `internal/jobs`: lifecycle-owned durable scheduling with one priority loop;
  due work is submitted to `internal/executor`, never to a second worker pool.
- `internal/ratelimit`: bounded sharded token buckets and expiration sweeping.
- `internal/config` and `internal/storage`: validated configuration, migrations,
  transactional namespaced state, and optional asset provider.
- `internal/inline`: Bot API adapter, bounded unit registry, callback/query router.
- `internal/modules`: built-in Go modules and lifecycle registry.
- `pkg/pluginproto/v1`: stable transport-neutral DTOs and RPC framing; no Telegram
  library imports. `sdk/go` is the external Go executable SDK.
- `internal/plugin`: executable supervision, handshake, RPC, health, restart.
- `internal/pluginpackage` and `internal/pluginruntime`: deterministic `.srnx`
  verification/installation and transactional package-to-process activation.
- `sidecar/python`: supervised compatibility host and the small Python SDK.
- `internal/observability`: structured logs, metrics, health, runtime/RSS sampling.

The core converts Telegram-library updates once into immutable protocol DTOs.
Filters are compiled at registration and evaluated in the core **before** an
update enters any plugin queue.

### Telegram authorization configuration

`internal/config.Telegram` is the library-independent authorization contract.
It contains an explicit enable switch, bounded API ID/hash, session file,
`phone` auth mode, device/application/system metadata, and locale metadata. A
relative session file is resolved beneath `data_dir` and traversal is rejected.
The phone number is never decoded from JSON: it is an optional ephemeral
environment or typed CLI override used only while creating a session. API hash
and phone are redacted by diagnostic formatting and field validation never
echoes their values. Login codes and two-factor passwords remain one-shot
adapter inputs and are never persisted in runtime configuration.

Disabled Telegram configuration keeps recorded-update operation credential-free.
Enabling the native transport requires an API ID/API hash pair and validates all
metadata before opening a network connection. QR is not part of the current
contract; adding it requires a separately tested adapter flow rather than a
configuration-only claim.

The native adapter pins `github.com/gotd/td` v0.145.1, which requires the Go 1.25
baseline. Interactive authorization is
owned only by `serenox login`; daemon startup supplies no prompt, verifies the
saved `Auth().Status`/`Self()` identity, and derives the owner ID from it. New,
edited, channel, and deleted-message updates enter one fixed-capacity channel;
blocking the gotd handler supplies cancellation-aware backpressure without a
goroutine per update. Outbound plain/HTML send, reply, edit, and delete resolve
peers through a bounded LRU learned from Telegram entities. The adapter never
fabricates user/channel access hashes, and unsupported inline/client/button
actions fail explicitly.

### Protocol v1

External Go plugins and the Python host connect to a core-created Unix socket in
a mode-0700 runtime directory. A `Transport` interface permits named pipes or
loopback transport on platforms without Unix sockets. The child receives only
the socket path, one-use authentication token, plugin instance ID, and supported
protocol range. The core owns the listener and removes it on every exit path.

RPC uses unsigned 32-bit big-endian length-prefixed JSON frames with a configured
maximum frame size. Every envelope contains `protocol`, `request_id`, `kind`,
`deadline_unix_ms`, and a typed payload. The handshake negotiates exactly one
major version before `Configure` and `Start`; incompatible peers fail with a
structured actionable error. V1 defines explicit DTOs for:

- `PluginMetadataV1` and handler/filter declarations;
- `MessageV1`, `UpdateV1`, and `CommandV1`;
- `ActionV1` (reply, edit, delete, send, inline answer, and supported client
  operations) plus `ActionResultV1`;
- typed `ConfigValueV1`, storage requests/results, and lifecycle messages;
- `RPCErrorV1` with stable code, message, retryability, and field details.

Frames, strings, collections, handler counts, action batches, and in-flight RPCs
all have configured hard limits. Updates are at-most-once: a crashed plugin is
not replayed automatically because its prior actions may have succeeded.
Registration and health calls are idempotent. Protocol additions are optional
fields or a new negotiated version; public DTO fields never expose Pyrogram,
Aiogram, or a chosen Go Telegram library.

### Plugin distribution: `.srnx` package v1

`.srnx` is the single user-facing plugin extension. It is a deterministic ZIP
container, not an executable format and not a trust boundary: the installer
recognizes content only after parsing and validating the canonical
`manifest.json` whose `format` is `serenox.plugin.package/v1`. Renaming an
arbitrary ZIP or binary to `.srnx` never bypasses verification.

The manifest fixes the plugin ID/version/category, compatible core and protocol
ranges, requested capabilities and permissions, platform entrypoints, and the
size, mode, and SHA-256 digest of every payload file. Package v1 supports:

- a native Go **executable** per exact `GOOS/GOARCH`, always launched through
  the process/RPC supervisor;
- a Python source entrypoint loaded by the built-in supervised sidecar process;
- several platform executables in one package when an author wants a universal
  download.

Go shared objects (`plugin.so`) are deliberately excluded: they couple the
plugin to one Go toolchain and dependency graph, execute in the core address
space, and turn a plugin fault into a daemon fault. The core also does not run a
compiler or `pip` while installing a package.

`signature.json` optionally carries an Ed25519 signature over the canonical
manifest digest. Policy is explicit (`allow-unsigned`, `require-signed`, or
`require-trusted`) and trusted public keys live outside the package. Verification
has fixed archive/file/count/uncompressed limits and rejects traversal,
symlinks, special files, duplicate/case-colliding names, undeclared content,
hash mismatches, incompatible ranges, and a missing platform entrypoint before
extraction.

Installation extracts only a verified open archive into a private staging
directory, fsyncs it, atomically renames it into
`plugins/<id>/versions/<version>`, and atomically changes the active-version
record. A bounded number of older versions is retained for rollback. Process
activation happens after installation; activation failure restores the prior
active version and process. Removing or rolling back a package never edits the
legacy Python tree.

The initial UX is one file and one command (`serenox plugin install plugin.srnx`),
with `verify`, `list`, `rollback`, and `uninstall` using the same implementation
as daemon-side management. A future catalog transports exactly these signed
bytes and does not introduce a second installation format. See
[`plugin-packages.md`](plugin-packages.md) for the schema and author workflow.

### Bounded execution and backpressure

- Telegram ingress is a fixed-capacity queue. Owner commands use a small reserved
  lane and bounded enqueue wait; ordinary watcher updates use non-blocking
  admission and are dropped/coalesced according to update kind when saturated.
  Every rejection increments a labeled metric.
- Each plugin/module has a fixed queue and a fixed worker count from validated
  limits. A fair scheduler prevents one plugin from consuming all action/RPC
  capacity. Commands receive a visible busy result when possible; watcher work
  is dropped on overload. Queue capacity is never grown at runtime.
- Built-ins run behind the same deadline, panic recovery, accounting, and worker
  limits as external plugins. External peers also have bounded concurrent RPC
  and action budgets. A timed-out handler is canceled; repeated violations trip
  a circuit breaker.
- Rate limiting uses monotonic token-bucket/GCRA state, a bounded sharded table,
  and one periodic expiration sweep. It creates no event-specific goroutine or
  timer. When the key cap is reached, expired/LRU entries are evicted and the
  event is conservatively limited.
- Every background component implements `Run(context.Context)` and/or
  `Close(context.Context)`. Goroutines are created only by an owning component,
  included in its wait group, and covered by shutdown/leak tests. Caches, inline
  units, logs, metrics labels, subprocess output, and retry histories are capped.

### Configuration and storage

- Configuration is typed, validated, redacted in logs, and loaded with precedence
  CLI > environment > new-project config > defaults. Reload applies only fields
  declared reloadable. Invalid values fail startup with field-specific errors.
- Namespaced plugin state is stored by a `Store` interface. The production local
  backend is a transactional embedded key/value database (bbolt), with schema
  version, bounded key/value sizes, mode-0600 files, a single bounded write
  coordinator, and atomic transactions. Tests use an in-memory implementation.
- A one-way importer reads legacy `config.json` and `config-<tg_id>.json` into a
  new database after validation and records the import version/hash. It never
  changes the legacy file. Asset storage is a separate optional interface.

### Lifecycle, inline support, and recovery

Registry states are `discovered -> configured -> starting -> ready -> stopping
-> stopped`, with `failed`/`suspended` side states. Names and handlers are unique;
core handlers cannot be replaced. Startup is dependency ordered and bounded;
shutdown reverses the order and stops admission before draining queues.

The inline adapter is optional. It preserves forms, lists, galleries, inline
queries, chosen results, callback actions, edits/deletes, owner/allow-list checks,
and explicit unavailable status. Unit count, buttons, pages, media entries, TTL,
and payload bytes are capped. One sweeper owns expiration; callbacks are opaque
action IDs rather than function pointers across RPC.

The supervisor starts each executable in its own process group, performs a
deadline-bound handshake, caps stderr/stdout, sends health probes, and terminates
gracefully before force-kill. Restarts use capped exponential backoff with jitter,
a finite retry window, and a circuit breaker. Malformed/oversized frames close
only that peer. Sidecar or plugin failure cannot stop the core.

Shutdown sequence: stop Telegram/inline admission; stop accepting plugin work;
drain within deadline; cancel handlers; send plugin `Stop`; terminate remaining
process groups; close sockets/transports; flush metrics/logs/storage; wait for all
owned goroutines. Integration tests assert no worker, child, socket, or temporary
runtime directory remains.

### Python runtime host

CPython is never linked or embedded. The supervised Python process uses the same
v1 RPC transport and the same package, install, update, rollback, unload, and
restore controller as native plugins. Its SDK implements:

- lifecycle hooks and command/watcher/inline/callback decorators;
- typed configuration and namespaced JSON storage;
- message reply/edit/delete and inline action proxies;
- a documented, finite set of client operations.

Decorator metadata is converted into v1 filter declarations so the Go core
filters before enqueue. The host has bounded per-module queues/workers and rejects
new work on overload. Unsupported imports, raw Telegram objects, arbitrary client
methods, remote library loading, or other absent APIs raise
`UnsupportedAPIError(api, replacement, compatibility_url)` at load/call time.
Compatibility is claimed per API only after automated Python fixtures and golden
legacy cases pass.

### Observability

Structured logs include account, plugin, handler, update/request, lifecycle state,
duration, and error code while redacting credentials and message bodies by
default. Bounded-cardinality metrics cover ingress, filtered/dropped updates,
queue depth/capacity, active workers, handler/RPC latency, timeouts, panics,
restarts, storage latency, goroutine count, heap, idle RSS, and peak RSS. Health
reports distinguish Telegram, inline, storage, and each plugin/sidecar.

### Optional control-bot transport

`internal/controlbot` is a core-owned Bot API adapter, independent of
`internal/telegram`'s user session. One verified BotFather token is stored in a
private account data file and is never placed in plugin configuration, a Python
sidecar environment, or a protocol DTO. The CLI's `serenox bot attach` calls
`getMe` before atomically replacing the local secret; the daemon starts long
polling only after its executor and plugin registry are ready.

Bot API updates are converted once into bounded `ControlEvent` DTOs. Private
commands and inline queries resolve only explicit `ControlCommand` declarations;
ordinary owner commands remain user-session-only. A control action requires the
`control_bot` capability, `telegram.bot.write_ui` permission, and an explicit
`response_channel: control_bot`. The application rejects any attempt to send it
through the user-session transport. Callback acknowledgements use a short core
deadline; callbacks are bound to the same bounded dynamic registry and are
cleared on plugin unload and daemon shutdown. Webhooks and arbitrary Bot API
method dispatch are intentionally unsupported.

## 3. Compatibility stance and non-goals

Preserve where fixtures verify it: owner-command parsing and aliases; default
prefix; watcher filter meaning; lifecycle order; namespaced JSON values; HTML
reply/edit semantics; core help/settings/loader/updater/diagnostics flows; and the
listed inline primitives.

Deliberate non-goals for the first compatibility release:

- binary/source compatibility with Pyrogram, Aiogram, Telethon, or arbitrary raw
  Telegram objects;
- full execution of every historical Python module or every legacy bug;
- in-process Python, runtime `pip` installation in the core, or downloaded Python
  execution in the Go address space;
- unbounded detached work, caches, timers, retries, inline units, or log buffers;
- built-in arbitrary Python evaluation or unrestricted shell/apt execution;
- preserving no-op security-decorator behavior as an authorization guarantee;
- hot replacement of the running core without a staged, verified artifact and
  supervisor-controlled rollback.

Raw-update plugins remain unsupported until each needed raw event has an explicit
versioned DTO. Unsupported behavior must return a stable actionable error, not a
silent partial emulation. “Full Hikka compatibility” must not appear in release
claims until the published compatibility matrix is entirely fixture-backed.

## 4. Migration order

1. Freeze the observed inventory and ADR; record legacy golden cases.
2. Scaffold the isolated Go project and contributor/CI tooling.
3. Freeze protocol v1 DTOs, framing, handshake, and lifecycle contracts.
4. Implement configuration, transactional storage, dispatch/filtering, bounded
   execution, and timer-free rate limiting with unit/overload tests.
5. Add Telegram transport behind an interface and replay recorded updates.
6. Add the supervised Python host and compatibility SDK with fixtures.
7. Port loader, help, settings/config, updater/restart, info/diagnostics, then
   required inline glue as built-in Go modules.
8. Add integration, malformed-RPC, crash/restart, shutdown, race, load, soak, RSS,
   allocation, latency, and throughput gates.
9. Publish setup, operations, architecture, contributor, Go/Python plugin, and
   compatibility documentation; cut releases only from passing gates.

## Moderation boundary

Administrative Telegram work crosses the dedicated `host.moderation` reverse
RPC and a bounded core service, never ordinary actions or the stringly client
operation payload. Domain-group backend interfaces isolate members, admins,
messages, settings, join requests, invites, and topics. See
[moderation-api.md](moderation-api.md) and the
[implementation matrix](moderation-implementation-matrix.md).
# Event Fabric

The single-daemon durable plugin composition path is specified in [Event Fabric](event-fabric.md). It reuses protocol framing, signed package admission, the per-plugin executor, file-backed storage, action results, and supervisor lifecycle; it does not introduce a broker or second plugin runtime.
