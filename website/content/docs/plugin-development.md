---
title: Plugin development guide
description: Choose a runtime and build, package, install, test, and remove a Serenox plugin.
---

# Plugin development guide

This guide takes plugin authors from source code to an installed `.srnx` package. Serenox plugins are supervised processes. They receive library-neutral DTOs and request explicit host actions; they never receive Telegram credentials, raw gotd objects, a shell, or unrestricted network access.

> [!IMPORTANT]
> Full Hikka, Pyrogram, Telethon, and Aiogram compatibility is not a goal. Check the [compatibility matrix](compatibility.md) before porting a module.

## Choose Go or Python

| Choose | When it fits | Runtime boundary |
|---|---|---|
| Go executable | You want a self-contained binary and direct use of the typed Go SDK | One supervised native child process per plugin |
| Python sidecar | You prefer async Python and decorators or are rewriting a legacy module | A managed, isolated interpreter started by the Serenox sidecar |

Both use protocol `serenox.plugin/1`, declare the same capabilities and permissions, and return validated actions. Go entrypoints are platform-specific executables. Python packages require Python 3.10 or newer and the managed sidecar; Serenox does not run `pip` on startup.

## Understand the lifecycle

1. The daemon verifies package ranges, signature policy, and file digests.
2. The supervisor starts the selected entrypoint with an authenticated local transport.
3. The plugin handshakes with its identity, handlers, and capabilities.
4. The core sends configuration and starts dispatch.
5. Matching work enters a bounded per-plugin queue.
6. The plugin returns actions or calls a permission-gated host service.
7. Stop, timeout, crash, rollback, or shutdown cancels owned work and closes resources.

Dispatch is at-most-once. The core does not replay an interrupted handler because an external effect may already have happened. Use stable request IDs and interpret `outcome_unknown` explicitly.

## Start from a tested example

- [`examples/plugins/time`](https://github.com/nihmadev/Serenox/tree/main/examples/plugins/time) is the smallest Go command plugin.
- [`examples/plugins/weather`](https://github.com/nihmadev/Serenox/tree/main/examples/plugins/weather) shows Python and bounded HTTP.
- [`examples/plugins/autopilot`](https://github.com/nihmadev/Serenox/tree/main/examples/plugins/autopilot) combines storage, jobs, actions, and recovery.
- [`examples/plugins/uwu`](https://github.com/nihmadev/Serenox/tree/main/examples/plugins/uwu) is a migration-oriented Python example.

Generate and inspect a migration starter:

```sh
bin/serenox plugin init --template uwu --output ./my-plugin
bin/serenox plugin preflight --source ./my-plugin
```

## Declare the package

Every source tree includes `manifest.json`. The packer fills `files`; authors declare identity, compatibility, entrypoints, capabilities, permissions, network policy, and resource limits.

```json
{
  "format": "serenox.plugin.package/v1",
  "id": "example.hello",
  "name": "Hello",
  "version": "1.0.0",
  "description": "Replies with a bounded greeting",
  "category": "go-executable",
  "protocol": {"min": "1.0", "max_exclusive": "2.0"},
  "core": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "entrypoints": [{"os": "linux", "arch": "amd64", "path": "bin/hello-linux-amd64"}],
  "capabilities": ["commands"],
  "permissions": ["telegram.reply"],
  "files": []
}
```

A capability describes what the plugin registers; it does not grant an action. Request the minimum permissions.

## Build and test locally

```sh
mkdir -p bin
go build -trimpath -o bin/serenox ./cmd/serenox

cd examples/plugins/time
../../../bin/serenox plugin install
```

For Python development:

```sh
make python-sync
cd sidecar/python
uv run python -m unittest discover -s tests -v
```

Exercise dispatch without Telegram:

```sh
cp config.example.json config.json
bin/serenox config check --config config.json
bin/serenox run --config config.json --self-id 7 \
  -recorded internal/telegram/testdata/updates.ndjson
```

## Package and install

```sh
bin/serenox plugin build --source ./my-plugin
bin/serenox plugin verify ./dist/example.hello-1.0.0.srnx
bin/serenox plugin install ./dist/example.hello-1.0.0.srnx
bin/serenox plugin list
```

Verification happens before extraction. The installer stages files, atomically activates the new version, and retains bounded rollback history. A failed live activation restores the previous version.

## Upgrade, roll back, and remove

Increase the package version whenever signed content changes. Then verify and install the new archive. Restore or remove with:

```sh
bin/serenox plugin rollback example.hello
bin/serenox plugin uninstall example.hello
```

Package removal and durable plugin data retention are separate concerns.

## Common failures

| Failure | Meaning | Next action |
|---|---|---|
| incompatible range | Core, protocol, SDK, or sidecar is outside the manifest range | Rebuild against the installed release or correct the range |
| permission denied | An action or service was not declared and admitted | Add only the required permission and rebuild |
| handshake rejected | Runtime metadata disagrees with the signed manifest | Align IDs, versions, handlers, and capabilities |
| queue overloaded | Work exceeded the per-plugin queue | Narrow filters or reduce handler latency |
| outcome unknown | A mutation may have reached Telegram | Reconcile state; do not retry blindly |
| Python runtime unavailable | No compatible managed environment exists | Run `make python-sync` and configure it |

Continue with [Go plugins](go-plugins.md), [Python plugins](python-plugins.md), [package reference](plugin-packages.md), and [operations](operations.md).
