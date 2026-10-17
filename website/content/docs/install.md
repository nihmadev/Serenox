---
title: Install Serenox
description: Build from source or verify and install a published Serenox release.
---

# Installing released CLIs

Releases publish one archive per operating system and architecture. Each
archive contains:

- `serenox` — the main user CLI, including `serenox plugin ...`;
- `serenoxd` — the long-running daemon;
- `serenox-plugin` — an optional standalone compatibility CLI for package
automation.

The installers download the matching archive and `checksums.txt`, verify the
SHA-256 digest before extraction, reject traversal paths, and replace binaries
through temporary files.

## Unix

```sh
curl -fsSL https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.sh | sh
```

Options can be passed to the downloaded script:

```sh
curl -fsSL https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.sh \
  | sh -s -- --version v0.1.0 --install-dir "$HOME/.local/bin"
```

Environment variables are useful for the pipe form:

```sh
export SERENOX_VERSION=v0.1.0 SERENOX_INSTALL_PLUGIN=0
curl -fsSL https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.sh | sh
```

## Windows PowerShell

```powershell
irm https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.ps1 | iex
```

The default destination is `%LOCALAPPDATA%\Serenox\bin`. Set environment
variables before invoking `irm`:

```powershell
$env:SERENOX_VERSION = 'v0.1.0'
$env:SERENOX_INSTALL_DIR = "$HOME\bin"
irm https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.ps1 | iex
```

`SERENOX_INSTALL_PLUGIN=0` installs only `serenox` and `serenoxd`; the main
CLI still manages plugins through `serenox plugin ...`.

## What plugin authors need

The release archive contains the CLI and core runtime, but not the Go/Python
SDK source or the Python sidecar. Therefore:

- you can start a Go plugin after installation by creating a separate Go
  module, adding `github.com/nihmadev/Serenox` as a dependency, and using
  `serenox plugin build` or `pack`;
- for Python, download the matching source archive and run `make python-sync`,
  unless a compatible sidecar is distributed separately;
- `serenox plugin pack`, `verify`, `install`, `list`, `rollback`, and
  `uninstall` work from the installed binary without a repository clone.

See the [plugin development guide](plugin-development.md) for both workflows.

## Release asset contract

The release workflow creates these names:

```text
serenox_linux_amd64.tar.gz
serenox_linux_arm64.tar.gz
serenox_darwin_amd64.tar.gz
serenox_darwin_arm64.tar.gz
serenox_windows_amd64.zip
serenox_windows_arm64.zip
checksums.txt
```

The scripts use the `latest/download` endpoint by default and accept an
explicit tag through `SERENOX_VERSION` or `--version`.
