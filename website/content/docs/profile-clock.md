---
title: Profile name and profile.clock
description: The bounded, lease-owned profile first/last-name contract.
---

# Profile name contract

Protocol v1 exposes one typed action, `profile_update_name`, with a
`ProfileNameAction` payload. Its operation is `acquire`, `update`, `restore`,
or `release`; update and restore carry the core-issued generation and opaque
lease token. Results contain only the bounded confirmed `first_name` and
`last_name` fields in `ProfileResult`.

The required permission is `telegram.profile.write`, and packages should also
declare the `telegram.profile` capability. Message-send and read-client
permissions do not imply it. Generic `client_operation` values cannot reach
the profile adapter.

At most one plugin owns the account-scoped lease. The core records the plugin
ID, account ID, generation, token, and the structured original/current name in
memory. A stale generation, token, duplicate writer, or account mismatch fails
closed. The core reads the current bounded identity before every mutation; if
the user changes the name outside Serenox, ownership is lost and the core does
not restore over that newer value.

`profile.clock` stores its original structured fields in its own namespaced
storage, acquires the lease, performs an immediate update, and uses one durable
core job with `skip_if_running` at a 60-second fixed-rate cadence. It formats
`original_first_name + " | " + HH:MM` in the configured IANA timezone, default
`UTC`; it never parses an existing `|`, so names containing separators are
preserved. It skips an equal formatted value and marks an ambiguous result as
uncertain rather than blindly retrying it.

The scheduler owns missed-tick, cancellation, overlap, and shutdown behavior.
The default misfire policy is `skip`; DST uses the scheduler's explicit
first-fold policy. Disable deletes the job, restores the structured original
name only after an ownership check, and releases the lease. Unload and daemon
shutdown make the same best-effort restore. A disconnect, timeout, flood wait,
authorization failure, invalid name, or response lost after possible Telegram
acceptance is reported with a stable typed error; Telegram profile updates are
sequential and are not transactional or exactly-once.

The Go SDK exposes `Actions.ProfileAcquire`, `ProfileUpdate`, `ProfileRestore`,
and `ProfileRelease`; Python plugins use `client.profile.acquire/update/restore/
release`. Recorded transport tests are credential-free replay tests and do not
claim live Telegram verification. A live smoke test must be separately gated
and restore the original name in cleanup.

## Install and operate

Build/package the example under `examples/plugins/profile-clock`, install the
resulting package, then use the owner command:

```text
/clock enable
/clock status
/clock disable
```

Removing or unloading the plugin invokes the same bounded cleanup path. If an
ambiguous result is reported, inspect the current profile before deciding
whether to re-enable; do not manually retry the same mutation blindly.
