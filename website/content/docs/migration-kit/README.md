---
title: Migration kit
description: Convert legacy modules into explicit, bounded Serenox plugins.
---

# Serenox migration kit

The migration kit is the supported path for users moving from Hikka- or
Heroku-style modules to Serenox. It is deliberately behavior-oriented: keep
the user-visible behavior, rewrite the implementation against a small typed
surface, and make every permission and refusal visible before installation.

Serenox does not promise drop-in compatibility with Pyrogram, Telethon,
Aiogram, Hikka, or Heroku. A migrated plugin should either use a documented
Serenox analogue or fail with a clear unsupported-operation message.

## The five-minute path

Create a safe starter plugin:

```sh
serenox plugin init --template uwu --output ./my-uwu
cd ./my-uwu
```

Run the migration preflight before editing or publishing:

```sh
serenox plugin preflight --source .
serenox plugin preflight --source . --json > preflight.json
```

The preflight performs two checks:

1. It sends the source tree through the same deterministic packer used by
   `serenox plugin pack`, so manifest, entrypoint, file, and package limits are
   checked early.
2. It scans Python source for common legacy boundaries and reports a
   replacement, inferred capabilities, and likely permissions. The scan is a
   conservative review aid, not a static proof of safety.

Then test and package the module:

```sh
make python-sync
serenox plugin pack --source . --output ../dist/my-uwu.srnx
serenox plugin verify ../dist/my-uwu.srnx
serenox plugin install ../dist/my-uwu.srnx
```

The generated `uwu` template demonstrates a common migration shape: an
owner-only command plus an incoming watcher, typed configuration, a bounded
per-chat cooldown, and only `telegram.reply` permission.

## How to use this kit

- Read the [behavior matrix](behavior-matrix.md) before choosing an API.
- Use the [rewrite cookbook](cookbook.md) to translate one behavior at a time.
- Treat every `unsupported` row as a product boundary, not as an invitation to
  smuggle a raw client through a generic operation.
- Add a fixture or a reference plugin when a migration pattern is important
  enough to support repeatedly.

## What belongs in a migrated plugin

Good migration candidates are bounded user-facing behaviors:

- owner commands and aliases;
- incoming/outgoing watchers with explicit filters;
- deterministic text replies and edits;
- small namespaced JSON state;
- allowlisted, read-only HTTP lookups;
- bounded media transforms;
- narrow moderation actions with explicit permissions;
- callbacks and inline units whose lifetime is safe to lose on restart.

Avoid rebuilding a general Telegram client inside the plugin. When a behavior
does not have a stable analogue, return an explicit explanation to the user or
leave that behavior out of the migration.

## Reference starter

The repository contains the same starter as a tested reference plugin:

[`examples/plugins/uwu`](../../../../examples/plugins/uwu)

It is intentionally small enough to understand in one sitting and strict
enough to demonstrate production habits: no raw client, no network, no dynamic
imports, no unbounded task creation, and no unbounded per-chat state.
