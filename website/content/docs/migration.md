---
title: Migrate from a legacy module
description: Rewrite legacy behavior against bounded Serenox APIs with rollback safety.
---

# Migration and rollback

The rewrite uses a separate configuration and data directory. The importer is
one-way and read-only with respect to legacy files.

1. Stop writes to the new Serenox store.
2. Hash `config.json` and `config-<account>.json` from the legacy installation.
3. Run the importer into an empty Serenox data directory.
4. Review its namespace/type/size report and rejected keys.
5. Start with recorded transport, then a test account, before the production
   account.
6. Keep the legacy runtime and files intact until the required compatibility
   fixtures pass.

Import never changes a legacy file. It records source hash, import schema, time,
and result in the new store so the operation is auditable and idempotent.

Rollback is operational: stop Serenox, restore the new-store backup if desired,
and start the still-intact Python implementation with its original configuration.
Actions already sent to Telegram are external side effects and are not reversed
by restoring local state.
