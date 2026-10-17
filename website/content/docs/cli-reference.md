---
title: CLI reference
description: Commands exposed by the current Serenox command-line interface.
---

# CLI reference

Run `serenox <command> --help` against your installed version for exact flags.

| Command | Purpose |
|---|---|
| `run` | Run in the foreground from JSON config or an application manifest |
| `daemon` | Alias for `run` |
| `login` | Authorize a Telegram user session |
| `bot` | Attach, verify, inspect, or detach the optional control bot |
| `events` | Inspect and manage the durable Event Fabric |
| `plugin` | Build, verify, install, load, list, roll back, or uninstall packages |
| `config check` | Validate effective configuration |
| `config effective` | Print redacted effective manifest configuration |
| `config migrate` | Convert legacy JSON to the application manifest |
| `manifest check` | Validate `serenox.yaml` without Telegram |
| `service check` | Check declared external services |
| `release` | Plan, apply, inspect, or roll back a release |
| `backup` | Create, verify, or restore a bounded backup |
| `version` | Print the version |

`run` accepts `-config`, `-manifest`, `-recorded`, `-self-id`, `-check-config`, and `-version`. Recorded mode requires `-self-id`.

`login` prompts for missing API credentials, phone, code, and optional 2FA. The API hash is not echoed or written to JSON. `bot attach` supports an interactive prompt, restricted token file, stdin, or a one-shot token; it never prints the token.

`plugin install` accepts an archive, a package directory, or no argument inside a plugin project. `plugin build` compiles the current-platform Go entrypoint; `plugin pack` packages already-built content.
