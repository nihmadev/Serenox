---
title: Configuration reference
description: Runtime keys, precedence, validation, defaults, and secret handling.
---

# Configuration reference

Serenox applies compiled defaults, JSON file, environment, then typed command overrides. Invalid values fail validation. Use `serenox config check` before startup and `serenox config effective` for a redacted manifest view.

The top-level JSON object contains `schema_version`, `data_dir`, `log_level`, `shutdown_timeout`, `storage`, `execution`, `jobs`, `rate_limit`, `dispatch`, `http`, `telegram`, and `plugins`. [`config.example.json`](https://github.com/nihmadev/Serenox/blob/main/config.example.json) is the copyable baseline; defaults are implemented in `internal/config.Defaults` and tested in `internal/config/config_test.go`.

## Secrets

Do not commit configuration with credentials, session files, bot tokens, or signing keys. Phone, login code, 2FA password, and bot token are not regular JSON fields. API credentials may use `SERENOX_TELEGRAM_API_ID` and `SERENOX_TELEGRAM_API_HASH`; session and private credential files live below `data_dir` with owner-only atomic writes.

| Group | Controls |
|---|---|
| `storage` | namespace, key, value, entry, and file ceilings |
| `execution` | plugins, per-plugin queue, workers, and handler timeout |
| `jobs` | jobs per plugin, intervals, payload, and horizon |
| `rate_limit` | token rate, burst, identities, expiry, sweep, and shards |
| `dispatch` | prefix and command, alias, and watcher limits |
| `http` | bytes, headers, concurrency, queue, rate, and burst |
| `telegram` | adapter, session, authorization mode, and client identity |
| `plugins` | trust, retained versions, loaded count, Python policy, and sidecar limits |
