# Setup

## Requirements

- Go 1.24 or newer;
- Python 3.10 or newer for the optional compatibility sidecar;
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

These are the required contributor gates, not a statement that the latest
gotd-enabled combined tree has passed them locally. The current local gotd
compile was stopped by the memory/OOM guard; full build, race, and `make check`
remain unchecked. When run, validation downloads pinned Go tools into
`.tools/bin` and creates the sidecar virtual environment. Generated binaries are
written to `bin/`; neither operation writes into the legacy `hikka/` tree.
For released binaries, use [`install.md`](install.md) for the Unix `curl` or
Windows PowerShell `irm` installer.

## Configuration

Start from [`config.example.json`](../config.example.json). Runtime precedence
is command-line values, environment, the JSON file, then defaults. Secret values
must be supplied through the environment or a mode-0600 file in production.

```sh
cp config.example.json config.json
bin/serenox config check --config config.json
bin/serenox run --config config.json --self-id 7 \
  -recorded internal/telegram/testdata/updates.ndjson
```

The recorded transport can be selected for deterministic development and does
not contact Telegram. The source-wired native live branch uses gotd/td and
requires the account's API ID, API hash, and saved session path.

The typed `telegram` block is disabled by default so recorded-fixture runs do
not require credentials. Set a positive numeric `api_id` and provide the
matching 32-character hexadecimal `api_hash` to authorize a phone session;
`serenox login` can do this while `enabled` is still `false`. Set `enabled` to
`true` before starting the native live transport. Relative `session_file`
values are kept beneath `data_dir`; the default resolves to
`data/telegram.session.json`. `auth_mode` currently accepts only `phone`.
Device, application, system, and language metadata are explicit and
length-validated rather than generated at each start.

All Telegram fields have `SERENOX_TELEGRAM_*` environment overrides. In
particular, production credentials can be injected without adding them to the
JSON file:

```sh
export SERENOX_TELEGRAM_ENABLED=true
export SERENOX_TELEGRAM_API_ID=123456
export SERENOX_TELEGRAM_API_HASH=0123456789abcdef0123456789abcdef
export SERENOX_TELEGRAM_SESSION_FILE=telegram.session.json
export SERENOX_TELEGRAM_PHONE=+15551234567
```

`SERENOX_TELEGRAM_PHONE` is optional after a session has been authorized and is
environment/CLI-override-only: a `phone` key in JSON is rejected. Configuration
errors and formatted configuration values redact both the API hash and phone.
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

This native source path and its gotd API usage have been audited, but the local
gotd compile and a real-account network fixture were not completed because of
the workspace memory/OOM guard. Plain/HTML reply/edit/send/delete mapping is
present in source; bounded peer resolution, unsupported buttons/inline/client
operations, and the remaining verification boundary are documented in
[`authentication.md`](authentication.md) for the exact verification boundary
and unsupported flows.

The `plugins` block fixes the maximum loaded process count and retains at least
two package versions so a failed process activation can restore the prior
version. `trust_mode` is `allow-unsigned`, `require-signed`, or
`require-trusted`; production installations should point `trust_file` at an
administrator-owned Ed25519 key document. A relative trust path is resolved
beneath `data_dir`. `python_executable` stays empty when legacy Python plugins are
disabled.

The build creates the canonical `bin/serenox` user CLI, the `bin/serenoxd`
service binary, and the compatible `bin/serenox-plugin` and `bin/srnx-plugin`
plugin-only aliases. New commands and documentation use `serenox plugin ...`.
Package install, verification, signing, and rollback are documented in
[`plugin-packages.md`](plugin-packages.md).

## Python sidecar

The sidecar is optional. Install its locked environment with:

```sh
make python-sync
```

The core supervises the sidecar and passes a local socket and one-use token.
Running a sidecar by hand is intended only for protocol tests; plugin modules
should never receive Telegram credentials directly.
