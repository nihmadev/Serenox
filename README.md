# Serenox

Canonical repository: <https://github.com/nihmadev/Serenox>.

Serenox is the bounded, process-isolated rewrite of the legacy Python Telegram
userbot. Go sources live at the repository root; the legacy `hikka/` source is
kept as a runnable behavioral reference and is never modified by the Go runtime.

The project is being delivered as tested vertical slices. See
[`docs/architecture.md`](docs/architecture.md) for the decisions and
[`docs/milestones.md`](docs/milestones.md) for verified gates.

## Current status

The daemon source selects either bounded recorded-update replay or a native
gotd/td user session. `serenox login --config config.json` is wired to the
phone/code/optional-2FA flow and persists the session; `serenox run` reuses that
session without interactive prompts and derives the owner ID from its verified
identity.

The gotd source/API audit passed, but the local gotd compile and a real-account
network fixture did not run to completion under the workspace memory/OOM guard.
The live source maps plain/HTML send, reply, edit, and delete through a bounded
peer cache; buttons, inline answers, client operations, and unresolved access
hashes fail explicitly. These paths are source-present rather than locally
compiled/fixture-verified. M5, the combined `make check`, race, build, and CI
gates remain unchecked in the milestone document.

See [`docs/authentication.md`](docs/authentication.md) for configuration, the
single-attempt login UX, session handling, and exact unsupported paths.

## Install released CLIs

The release contains the main `serenox` CLI, `serenoxd`, and the optional
standalone `serenox-plugin` compatibility CLI. The main CLI also exposes
`serenox plugin ...`, so the standalone plugin binary is not required.

Unix:

```sh
curl -fsSL https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.ps1 | iex
```

Set `SERENOX_VERSION`, `SERENOX_INSTALL_DIR`, and
`SERENOX_INSTALL_PLUGIN=0` to select a release, destination, or omit the
standalone compatibility binary. Installers verify the downloaded archive
against the release `checksums.txt` before replacing binaries.

See [`docs/install.md`](docs/install.md) for version selection, destinations,
and the release asset contract.

## Plugins

Users distribute one `.srnx` file; it is a deterministic, versioned ZIP package
rather than a Go shared object. `serenox plugin install` verifies it before
extraction and manages the active on-disk version. When a package is loaded by
the daemon, native Go plugins run as supervised RPC executables; Python plugins
require the separately configured isolated sidecar.

```sh
bin/serenox plugin verify --trust trust.json --trust-mode require-trusted plugin.srnx
bin/serenox plugin install --trust trust.json --trust-mode require-trusted plugin.srnx
bin/serenox plugin list
bin/serenox plugin rollback plugin.id
bin/serenox plugin uninstall plugin.id
```

`serenox` is the canonical user CLI. `serenoxd` remains the service binary;
`srnx-plugin` and `serenox-plugin` are compatibility aliases for existing
automation. New documentation and scripts should use `serenox plugin ...`.

The CLI commands above manage verified package versions and the active pointer;
they do not directly start a daemon process. The built-in command
`.loadmod /path/to/plugin.srnx` uses the same verifier, installs the package,
and starts its supervised process inside a running daemon.

See [`docs/plugin-packages.md`](docs/plugin-packages.md) for the exact format,
trust modes, limits, and installation layout, and
[`docs/go-plugins.md`](docs/go-plugins.md) for a compiling external plugin.

## Contributor commands

```sh
make fmt
make fmt-check
make lint
make test
make test-race
make build
make check
```

`make check` is the same non-destructive validation run by CI.
