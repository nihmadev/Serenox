---
title: Recorded-mode quickstart and configuration
description: Build Serenox and complete a safe first run without contacting Telegram.
---

# Setup

## Requirements

- Go 1.25 or newer;
- Python 3.10 or newer for the built-in Python runtime;
- [uv](https://docs.astral.sh/uv/) for pinned Python development tools;
- a Unix-like host for the current local-socket implementation. The transport
  interface is portable, but a Windows named-pipe implementation is not yet in
  the verified compatibility set.

## Build and validate

From this directory:

```sh
make build
make check
```

`make check` is the intended contributor/release gate. In the current checkout,
the Go tests, race tests, Python suite, build, and formatting checks pass; the
gate is not fully green because `make lint` currently reports staticcheck
findings in the Go tree. See [`milestones.md`](milestones.md) for the exact
snapshot and the opt-in live Telegram gate.

Validation downloads pinned Go tools into `.tools/bin` and creates the sidecar
virtual environment. Generated binaries are written to `bin/`; neither
operation writes into the legacy `hikka/` tree. For released binaries, use
[`install.md`](install.md) for the Unix `curl` or Windows PowerShell `irm`
installer.

## Configuration

For new installations, prefer the declarative [`application-manifest.md`](application-manifest.md)
workflow and [`serenox.example.yaml`](../../../serenox.example.yaml). The existing
JSON configuration remains supported for compatibility.

Start from [`config.example.json`](../../../config.example.json). Runtime precedence
is command-line values, environment, the JSON file, then defaults. Secret values
must be supplied through the environment or a mode-0600 file in production.

```sh
cp config.example.json config.json
bin/serenox config check --config config.json
bin/serenox run --config config.json --self-id 7 \
  -recorded internal/telegram/testdata/updates.ndjson
```

The recorded transport can be selected for deterministic development and does
not contact Telegram. The native live branch uses gotd/td and requires the
account's API ID, API hash, and saved session path.

The typed `telegram` block is live by default; recorded-fixture runs explicitly
select the offline adapter and do not require credentials. `serenox login` interactively requests a positive
numeric `api_id` and matching 32-character hexadecimal `api_hash` when they
are missing; the API hash is hidden on a terminal and neither value is written
to the JSON config. Use `--api-id` for the non-secret ID or environment
variables for both values. Running `serenox run` without `-recorded` starts the
live transport. Relative `session_file` values are kept beneath `data_dir`; the
default resolves to `data/telegram.session.json`. `auth_mode` currently accepts
only `phone`.
Device, application, system, and language metadata are explicit and
length-validated rather than generated at each start.

All Telegram fields have `SERENOX_TELEGRAM_*` environment overrides. In
particular, production credentials can be injected without adding them to the
JSON file:

```sh
export SERENOX_TELEGRAM_ENABLED=true
export SERENOX_TELEGRAM_API_ID=123456
export SERENOX_TELEGRAM_API_HASH='<api-hash>'
export SERENOX_TELEGRAM_SESSION_FILE=telegram.session.json
export SERENOX_TELEGRAM_PHONE=+15551234567
```

`SERENOX_TELEGRAM_PHONE` is optional after a session has been authorized and is
environment/CLI-override-only: a `phone` key in JSON is rejected. Configuration
errors and formatted configuration values redact both the API hash and phone.
Phone input accepts common separators and normalizes `+1 323 705 3776`,
`1-323-705-3776`, and `0013237053776` to E.164; country-code inference is not
performed.
The login code and optional two-factor password are interactive authorization
inputs and are not persistent configuration fields.

Authorize once through the canonical CLI, then start the noninteractive daemon:

```sh
bin/serenox login --config config.json
bin/serenox run --config config.json
```

`serenox login` performs one phone/code/optional-2FA attempt per invocation; it
does not automatically resend a code or retry a failed flow. The daemon passes
no prompt to the authenticator, verifies the identity stored in the session,
and derives the owner ID from it. A missing or unauthorized session exits with
an instruction to run `serenox login` rather than prompting on daemon stdin.

The native source path is compiled and covered by adapter tests. Plain/HTML
reply/edit/send/delete mapping, bounded peer resolution, and callback/URL/switch
buttons are implemented. A real-account network fixture remains an explicit
operator gate; unsupported inline/client operations and the exact live boundary
are documented in [`authentication.md`](authentication.md).

The `plugins` block fixes the maximum loaded process count and retains at least
two package versions so a failed process activation can restore the prior
version. `trust_mode` is `allow-unsigned`, `require-signed`, or
`require-trusted`; production installations should point `trust_file` at an
administrator-owned Ed25519 key document. A relative trust path is resolved
beneath `data_dir`. When `python_executable` is empty, a development checkout
automatically uses the managed `sidecar/python/.venv/bin/python` when present,
then probes `python3`. Set `python_managed_runtime` or `python_executable`
explicitly for a packaged or system deployment. The daemon probes Python,
sidecar, SDK, and protocol versions before a Python package can be installed
or restored.

The canonical `bin/serenox` binary contains the user CLI and the runtime and is
all a normal installation needs. `bin/serenoxd` remains an optional standalone
service entrypoint; `bin/serenox-plugin` and `bin/srnx-plugin` are compatible
plugin-only aliases. New commands and documentation use `serenox plugin ...`.
Package install, verification, signing, and rollback are documented in
[`plugin-packages.md`](plugin-packages.md).

## Python sidecar

Install the pinned managed Python environment with:

```sh
make python-sync
```

The core supervises the sidecar and passes a local socket and one-use token.
The child receives an isolated allowlisted environment, the verified package
directory as its working directory, and finite queue/worker/action/storage/
callback/frame/time limits. Dependencies are package metadata only; startup
never performs network installation.
Running a sidecar by hand is intended only for protocol tests; plugin modules
should never receive Telegram credentials directly.

The default terminal log format is compact and human-readable. Set
`SERENOX_LOG_FORMAT=json` when a service manager or log collector needs the
structured JSON format.
# Control bot (optional)

Create a bot with BotFather, enable inline mode if inline control commands are
needed, then attach it to one Serenox account data directory:

```sh
serenox bot attach
```

This opens a terminal dialog for the token (hidden on a TTY), expected
`@username`, and a final confirmation. For non-interactive automation use
`--token-stdin` or `--token-file`.

The verification call occurs before the token is written. The secret is kept in
`<data_dir>/control-bot.token` with mode 0600 rather than `config.json`; do not
back it up into source control. Use `serenox bot verify`, `serenox bot status`,
and `serenox bot detach` for lifecycle checks. The CLI does not run a polling
process: daemon lifecycle owns that transport, preventing a second local
process from competing for `getUpdates`.
