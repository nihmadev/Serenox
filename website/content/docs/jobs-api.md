---
title: Jobs API
description: Schedule bounded, durable plugin work owned by the Serenox core.
---

# Jobs API

Jobs are owned by one plugin/account identity (`plugin_id`, `account_id`, and
stable plugin-local `id`). A plugin declares job handlers in handshake metadata
and registers schedules through `host.jobs`. The core validates the declaration
again at registration, owns the schedule and status, and submits due work to
the existing per-plugin executor. A job cannot address another plugin or
account.

Protocol v1 uses `host.jobs` with an explicit `operation` and typed payload:
`create`, `update`, `cancel`, `delete`, `pause`, `resume`, `trigger`, `get`, `list`,
`inspect`, and `clear`. Payloads are JSON and capped at 64 KiB; listings are capped at 100
items and each plugin has at most 256 jobs by default. Errors include stable
job-invalid, duplicate, not-found, stale-revision, overloaded, handler-
unavailable, and interrupted categories.

Schedules are deliberately finite:

- `one_shot` runs no earlier than `run_at_unix_ms` and is limited to the
  configured future horizon.
- `fixed_delay` calculates its next run after the previous run finishes.
- `fixed_rate` follows the timeline and advances past missed occurrences
  without creating a backlog.
- `calendar` supports bounded seconds, minutes, hours, day-of-month,
  weekday (0–6), and month fields. Empty fields mean “any”.

Every schedule has an explicit IANA timezone, deadline, maximum runtime,
misfire policy, and overlap policy. DST gaps are rejected by default; callers
may explicitly request forward shifting. Folds must select first or second
occurrence. The scheduler has one lifecycle-owned loop and a priority heap; it
does not create a timer or goroutine per job. `skip_if_running`,
`coalesce_one_pending`, and `queue_one_bounded` keep overlap bounded, while
executor queue-full admission is reported as rejected.

Persistent records contain only the definition, ownership, revision, next-run
time, counters, last attempt, and a bounded history of summaries. They are
stored through the existing atomic storage commit path. Restore never claims a
run that was active at crash time succeeded: such a run is marked
`interrupted`. Plugin unload, uninstall, account detach, and daemon shutdown
stop admission and prevent a scheduled run from outliving its owner. Failed
activation rolls back handler declarations and job admission together.

Existing Telegram ingress, watcher, command, inline, callback, and lifecycle
work remains on the existing bounded queues. Legacy detached loops and
sidecar-created asyncio tasks are not silently migrated; they remain legacy
behavior until explicitly rewritten as a declared job. Python job calls use
the sidecar's owned event loop and reverse-RPC pending map, so cancellation and
disconnect cleanup do not leave unmanaged tasks.

## Minimal declarations

Python handlers are opt-in and are advertised only when decorated:

```python
from serenox_sdk import JobSpec
from serenox_sdk.loader import job

@job(name="cleanup", max_runtime_ms=10_000)
async def cleanup(self, invocation):
    return {}

status = await self.jobs.create(JobSpec(
    job_id="daily-cleanup",
    handler="cleanup",
    schedule={"kind": "fixed_delay", "interval": {"interval_ms": 86_400_000}},
))
```

Go plugins can route multiple declared handlers without starting goroutines:

```go
metadata.Jobs = []protocol.JobHandlerSpec{
    plugin.NewJobHandlerSpec("cleanup", "Remove expired state"),
}
handler := plugin.HandlerFuncs{JobFuncs: map[string]func(context.Context, protocol.JobInvocation) (protocol.DispatchResponse, error){
    "cleanup": runCleanup,
}}
```

The plugin must also declare the `jobs` capability and `jobs.schedule`
permission. The core overwrites plugin/account identity fields and enforces the
declared handler runtime, persistence, overlap, and payload ceilings.
