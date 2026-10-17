---
title: Application manifest
description: Configure a complete Serenox application with serenox.yaml.
---

# Application manifest

`serenox.yaml` is the declarative application and deployment manifest. It is
separate from the signed `manifest.json` inside a `.srnx` plugin package.

The schema is `apiVersion: serenox.app/v1`, `kind: Application`. The required
domains are `metadata`, `runtime`, `telegram`, `storage`, `services`,
`plugins`, and `release`. The checked-in [`serenox.example.yaml`](../../../serenox.example.yaml)
is a standalone file-storage example.

## Precedence and validation

The effective order is explicit CLI overrides, typed environment overrides and
resolved secret references, `serenox.yaml`, legacy `config.json`, then built-in
defaults. Automatic discovery uses `serenox.yaml` only when the current
directory has no `config.json`; otherwise a path must be supplied.

```sh
serenox manifest check --file serenox.yaml
serenox config effective --manifest serenox.yaml
serenox config migrate --from config.json --to serenox.yaml
serenox run --manifest serenox.yaml
```

YAML is bounded to 1 MiB, 32 nesting levels, 10,000 nodes, 2,048 collection
items/fields, and 16 KiB scalar values. Unknown and duplicate fields,
anchors/aliases, multiple documents, and numeric durations are rejected.

Telegram `apiId` accepts a decimal integer or `${ENV_NAME}`. `apiHash` accepts
only an environment reference or a `{file: relative/path}` reference. Secret
files must be regular mode-0600 files beneath `runtime.secretDir` (default
`<dataDir>/secrets`). Missing, malformed, traversing, or insecure references
fail before Telegram admission. Effective output, plans, release records,
logs, generated deployment files, and backups redact or exclude secret values.

## Services and storage

`file` is the default and current standalone backend. It retains the existing
bounded atomic JSON `FileStore` semantics. `postgres`, `redis`, and
`object_storage` declarations are typed and validated; this checkout reports
them as partial/unsupported because no PostgreSQL driver, Redis lease backend,
or S3 adapter is enabled. A required unsupported/unreachable service prevents
service checks and release plans from becoming ready. No credentials or raw
clients cross the plugin boundary.

```sh
serenox service check --manifest serenox.yaml
```

## Releases, backups, and rendering

Release plans use the canonical manifest digest. Apply requires `--yes`, writes
bounded redacted release records atomically, stages the manifest, and is
idempotent for an already healthy digest. Failed package verification or
activation leaves the previous active pointer unchanged. Rollback selects a
retained healthy release; external side effects are not undone.

```sh
serenox release plan --manifest serenox.yaml
serenox release apply --manifest serenox.yaml --yes
serenox release status --manifest serenox.yaml
serenox release rollback --manifest serenox.yaml
serenox backup create --manifest serenox.yaml
serenox backup verify --manifest serenox.yaml --input backup.srb
serenox backup restore --manifest serenox.yaml --input backup.srb
serenox manifest render --file serenox.yaml --format systemd
serenox manifest render --file serenox.yaml --format compose
```

Backups use `serenox.backup/v1`, checksummed tar/gzip entries, a 64 MiB default
total limit, atomic staging, and path validation. File state and bounded
release records are included; Telegram sessions and control-bot credentials
are excluded. Restore refuses an existing local control socket and replaces
state only after validation. Secret-inclusive encrypted backups are not
implemented.

Renderers generate files only. They never deploy resources or execute commands.
Systemd uses the existing `serenox run --manifest` lifecycle. Compose can
render declared PostgreSQL, Redis, and MinIO service shells, but those images
do not make the corresponding Serenox adapters supported.
# Event Fabric

The Event Fabric currently uses fixed, conservative daemon defaults and the configured file-backed host store. Plugin event authority belongs in each signed `.srnx` manifest, not in `serenox.yaml`. Multi-node or broker configuration is intentionally unsupported.
