# Serenox

## Optional control bot

Serenox can attach one owner-controlled Telegram bot as a bounded UI transport.
It is not a second user session and plugins never receive its token or a Bot API
client. Attach a token securely (BotFather must have inline mode enabled when
you intend to use `@bot query`):

```sh
serenox bot attach
serenox bot verify
serenox bot status
serenox bot detach
```

`attach` opens an interactive dialog for the BotFather token, expected `@username`,
and confirmation; it verifies `getMe` before replacing the private account-local secret;
the token is neither stored in JSON configuration nor printed. `--token-file`
and the explicit, one-shot `--token` form are also available. The daemon owns
polling; attaching does not start a background process from the CLI.

**A Telegram userbot core with hard limits, isolated plugins, and an honest compatibility boundary.**

Serenox is a ground-up Go rewrite of the Hikka-style userbot runtime. It keeps
the useful part of the old model — owner commands, watchers, and small
installable modules — while moving risky work out of the core process:

- Go plugins run as supervised RPC executables;
- Python plugins run in the managed sidecar, not in the daemon interpreter;
- queues, workers, storage, callbacks, media, and HTTP are bounded;
- Telegram objects cross the boundary only as versioned protocol DTOs.

This is a development-ready core, not a claim of full Hikka compatibility. The
recorded Telegram path and core/plugin tests are real; live-account smoke tests
are opt-in and are not run during an ordinary build.

## The point: a plugin crash should not kill the userbot

| Concern | Legacy in-process runtime | Serenox |
|---|---|---|
| Plugin failure | A module can take down the host | The supervisor can restart or isolate one plugin |
| Event load | Detached work can grow with traffic | Fixed queues and workers apply backpressure |
| Persistent state | Whole-file rewrites are easy to corrupt | Namespaced, bounded, atomic storage |
| Delivery | Source files and runtime dependency installs | Deterministic, verifiable `.srnx` packages |
| Telegram API | Library objects leak into modules | Stable, library-neutral protocol DTOs |
| Network access | Module-defined and difficult to audit | Allowlisted, outbound-only host HTTP service |
| Diagnostics | Limited visibility into stuck modules | Queue, health, rate-limit, heap, RSS, and restart data |

The trade-off is deliberate: Serenox does not expose shell/eval, arbitrary
Pyrogram or Aiogram APIs, runtime `pip`, raw Telegram objects, or a promise of
drop-in Hikka modules. Unsupported operations fail explicitly instead of
pretending that an action succeeded.

## What works today — and what is still behind a warning sign

| Surface | Current state | Evidence or boundary |
|---|---|---|
| Core dispatch | Working | Owner-only commands, aliases, configurable prefix, compiled watcher filters |
| Recorded Telegram | Working | Deterministic NDJSON replay, command/action fixtures, clean shutdown tests |
| Live Telegram transport | Implemented, smoke-gated | Phone/code/optional-2FA login, saved session, updates, text actions; real-account test is opt-in |
| Plugin lifecycle | Working | Verify/install/load/restart/rollback/uninstall, process supervision and circuit breaker |
| Go plugins | Working | External RPC executable SDK and `examples/plugins/{echo,time,autopilot}` |
| Python plugins | Working | Built-in sidecar, decorators, lifecycle, typed config, async storage and HTTP |
| Storage | Working | Transactional namespaced JSON with cardinality, size, and file limits |
| Host HTTP | Working | Signed origin allowlists, bounded requests/responses, no ambient proxy or sockets |
| Host media | Working in the tested bridge | Streaming inspect/download/upload/attach/delete with ownership and quotas |
| Inline/client APIs | Limited | DTOs and bounded proxies exist; broad Telegram client surface is unsupported |
| Full Hikka compatibility | Not a goal of v1 | Use the compatibility matrix for each individual behavior |

### Validation snapshot

The current checkout was checked during this documentation pass:

| Check | Result |
|---|---|
| `go test ./...` | Pass |
| `go test -race ./...` | Pass |
| Python unittest suite | Pass — 31 tests |
| `make build` | Pass |
| `make fmt-check` | Pass |
| `make lint` / full `make check` | Not clean — staticcheck reports existing Go findings; see [Contributing](website/content/docs/contributing.md) |

## Run it without Telegram in two minutes

Requirements: **Go 1.25+**. Python **3.10+** and
[`uv`](https://docs.astral.sh/uv/) are needed for the managed Python runtime and
the full development checks; the recorded Go-only path below needs only Go.

```sh
git clone https://github.com/nihmadev/Serenox.git
cd Serenox

mkdir -p bin
go build -trimpath -o bin/serenox ./cmd/serenox
cp config.example.json config.json
bin/serenox config check --config config.json
bin/serenox run --config config.json --self-id 7 \
  -recorded internal/telegram/testdata/updates.ndjson
```

The recorded adapter never contacts Telegram and is the fastest way to inspect
the command pipeline, plugin dispatch, action results, and shutdown behavior.

## Connect a real Telegram account

Create an application at <https://my.telegram.org/apps>, then authorize once:

```sh
bin/serenox login --config config.json
bin/serenox run --config config.json
```

`login` asks for a missing API ID, API hash, phone, code, and — when required —
2FA password. The hash is hidden in a terminal. Phone, code, and 2FA values are
not written to the main JSON config; the session is stored atomically beneath
`data_dir`. For non-interactive setup, provide `SERENOX_TELEGRAM_API_ID` and
`SERENOX_TELEGRAM_API_HASH`, and use the separate credentials/session guidance
in [Setup](website/content/docs/setup.md) and [Telegram authorization](website/content/docs/authentication.md).

The daemon does not prompt on stdin. If there is no authorized session, it
stops and tells you to run `serenox login` first. QR login, sign-up, passkeys,
and automatic code retries are intentionally unsupported.

## Built-in commands worth knowing

The default prefix is `.` and built-in commands cannot be replaced by external
plugins.

| Module | Commands |
|---|---|
| Loader | `.loadmod`, `.unloadmod`, `.plugins`, `.clearmodules` |
| Help | `.help`, `.helphide` |
| Settings | `.setprefix`, `.aliases`, `.addalias`, `.delalias`, `.watchers` |
| Updater | `.restart`, `.source` |
| Diagnostics | `.ping`, `.diagnostics`, `.diag` |

Use `.help <module-or-command>` for the command-specific description exposed by
the running registry.

## Ship a plugin as one file

`.srnx` is a deterministic ZIP package containing a canonical manifest,
platform entrypoints, file digests, compatibility ranges, permissions, and an
optional Ed25519 signature. Verification happens before extraction; renaming an
arbitrary archive does not make it a plugin.

| Plugin kind | Process | Best for | Included reference |
|---|---|---|---|
| Go executable | Supervised native child process | Fast, self-contained modules | `echo`, `time`, `convert`, `weather`, [`autopilot`](examples/plugins/autopilot/README.md) |
| Python sidecar | Supervised managed Python process | Python modules and gradual migration | `examples/plugins/weather/plugin.py` |

For a local Go example:

```sh
cd examples/plugins/time
../../../bin/serenox plugin install
```

The developer install command discovers the project root, builds the current
platform entrypoint, creates an archive, and installs it into the same data
layout used by `serenox run`. For an already-built payload, use the explicit
package lifecycle:

```sh
bin/serenox plugin verify dist/example.echo-1.0.0.srnx
bin/serenox plugin install dist/example.echo-1.0.0.srnx
bin/serenox plugin list
bin/serenox plugin rollback example.echo
bin/serenox plugin uninstall example.echo
```

When the daemon is running, install can hot-load the package through its local
mode-0600 control socket. A failed activation restores the previous active
version; a plugin never receives Telegram credentials or library-native
objects.

## The architecture in one screen

```text
Telegram / recorded NDJSON
          │
          ▼
  bounded ingress + filters
          │
          ▼
 commands / watchers / rate limits
          │
    ┌─────┴──────────────┐
    ▼                    ▼
built-in Go modules   per-plugin queues
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
       Go RPC executable     Python sidecar
              │                     │
              └──────────┬──────────┘
                         ▼
              validated actions + host services
```

The core owns Telegram access, filtering, storage, process lifecycle, and
resource limits. Plugins talk through the authenticated versioned
[`pluginproto/v1`](pkg/pluginproto/v1) contract. The [architecture guide](website/content/docs/architecture.md)
explains why the boundaries are shaped this way.

## Read the right guide next

| If you want to… | Read |
|---|---|
| Install, configure, or authorize an account | [Setup](website/content/docs/setup.md), [Telegram authorization](website/content/docs/authentication.md) |
| Write a plugin from scratch | [Plugin development guide](website/content/docs/plugin-development.md) |
| Understand `.srnx`, signatures, and rollback | [Plugin package format](website/content/docs/plugin-packages.md) |
| Write a Go plugin | [Go plugin guide](website/content/docs/go-plugins.md) |
| Write a Python plugin | [Python plugin guide](website/content/docs/python-plugins.md) |
| Use actions, idempotency, or media | [Action results](website/content/docs/action-results.md), [Media API](website/content/docs/media-api.md) |
| Call an external web API safely | [Host HTTP client](website/content/docs/http-client.md) |
| Run a durable workflow/crash-recovery demo | [Autopilot guide](website/content/docs/autopilot.md) |
| Diagnose overload or process restarts | [Operations](website/content/docs/operations.md) |
| Migrate from the legacy runtime | [Migration guide](website/content/docs/migration.md) |
| Check what is genuinely supported | [Compatibility matrix](website/content/docs/compatibility.md) |

## Sharp edges, documented on purpose

Telegram mutations are sequential, not transactional. An `outcome_unknown`
result is never retried blindly. Media handles expire with their owner and
uploaded bytes are quota-bound. HTTP writes have no automatic retry. Python
dependencies are package metadata, not an invitation to install from the
network at startup. These constraints are part of the runtime contract, not
missing polish.

There is no published release archive at the moment; build from source or use
the installer instructions in [Install](website/content/docs/install.md) when a tagged release
is available.

## Contributing

```sh
make fmt
make test
make test-race
make build
make check
```

The legacy `hikka/` tree is a read-only behavioral reference. See
[Contributing](website/content/docs/contributing.md) before changing protocol, lifecycle,
compatibility, or resource-ownership behavior.

## License

No license file is currently included. The source is publicly visible, but no
reuse rights are granted by default until a license is added.

Typed Telegram moderation is documented in
[moderation API](website/content/docs/moderation-api.md), with an honest per-operation
[implementation matrix](website/content/docs/moderation-implementation-matrix.md).
