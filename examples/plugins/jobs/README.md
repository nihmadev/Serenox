# Jobs Lab example plugin

`example.jobs` is an interactive smoke-test plugin for the first-class Jobs
API. It deliberately uses only the bounded Python SDK: no background tasks,
timers, retry loops, or direct Telegram objects are created by the plugin.

## Manual flow

After loading the plugin, run these owner commands:

```text
.jobsdemo start
.jobsdemo status
.jobsdemo get demo-rate
.jobsdemo inspect demo-rate
.jobsdemo trigger demo-rate
.jobsdemo pause demo-micro
.jobsdemo resume demo-micro
.jobsdemo update demo-rate
.jobsdemo clear demo-rate
.jobsdemo cancel demo-session
.jobsdemo todo add 30 Check the durable one-shot reminder
.jobsdemo todo list
.jobsdemo delete demo-session
.jobsdemo cleanup
```

The bundle exercises fixed-rate, fixed-delay, calendar, one-shot, persistent,
and session-only jobs. `fixed_delay_probe` accepts a bounded `sleep_ms` payload;
the other probes update namespaced storage counters. `todo_reminder` sends one
bounded client message and stores a completion marker. `status` exposes
revision, run, missed, rejected, and active counters.

The example is intentionally useful with both a recorded transport and a live
account. Use a short todo delay first, then restart/unload the plugin and run
`status` again to verify persistent jobs restore while `demo-session` does not.
