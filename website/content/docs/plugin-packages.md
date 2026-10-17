---
title: Plugin packages, signing, and lifecycle
description: Build deterministic .srnx packages and manage verification, upgrades, and rollback.
---

# Serenox plugin packages (`.srnx`)

> If you are authoring your first plugin, read the
> [complete plugin development guide](plugin-development.md) first. This page
> is the detailed package, signing, and installation reference.

## Decision

Release plugins as one file named `<plugin-id>-<version>.srnx`. The `.srnx`
suffix is the user-facing convention; the bytes are a deterministic ZIP
archive and the verifier identifies the package from its contents, never from
the filename. A renamed `.zip` does not become trusted, and a valid package can
still be verified if it has another suffix.

Source directories remain the convenient development format. Authors publish a
`.srnx` artifact; users do not copy loose executables or Python files into the
data directory. Installation expands the verified artifact into a private,
versioned directory so upgrades and rollbacks do not overwrite the active
version in place.

The format supports exactly two process-isolated categories:

- `go-executable`: one or more native executables, each tied to an exact
  `GOOS/GOARCH` pair;
- `python-sidecar`: a Python source entrypoint run by the built-in supervised
  compatibility host. An `any/any` entrypoint is allowed.

Go shared objects (`.so` files loaded through package `plugin`) are not a plugin
category. They bind authors to the core's precise Go toolchain and dependency
graph, share the daemon's address space, and make process-level recovery
impossible. A Go plugin is an ordinary executable speaking the versioned local
RPC protocol.

## Archive layout

```text
manifest.json                    required, canonical JSON
signature.json                   optional Ed25519 signature
bin/echo-linux-amd64             payload declared by manifest.json
bin/echo-darwin-arm64            another optional platform payload
README.md                        optional payload, also declared and hashed
```

There are no directory entries. Every entry other than the two reserved root
metadata names must appear exactly once in `manifest.files`. The packer:

1. walks regular source files without following symlinks;
2. records their byte size, SHA-256 digest, and normalized mode (`0644` or
   `0755`);
3. sorts files, entrypoints, capabilities, and permissions;
4. serializes `manifest.json` in its canonical compact form;
5. writes `manifest.json`, optional `signature.json`, then sorted payloads with
   DEFLATE compression and the fixed UTC timestamp `1980-01-01T00:00:00Z`;
6. fsyncs a temporary file and replaces the requested output.

With the same source bytes, manifest values, key, and pinned Go toolchain, two
pack operations produce byte-identical archives. The packer replaces the
template's `files` value; authors do not calculate file hashes by hand.

## Manifest v1

The schema identifier is `serenox.plugin.package/v1`. Unknown JSON fields are
rejected.

```json
{
  "format": "serenox.plugin.package/v1",
  "id": "example.echo",
  "name": "Echo",
  "version": "1.0.0",
  "description": "Replies with its arguments",
  "category": "go-executable",
  "protocol": {"min": "1.0", "max_exclusive": "2.0"},
  "core": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "entrypoints": [
    {"os": "linux", "arch": "amd64", "path": "bin/echo-linux-amd64"}
  ],
  "capabilities": ["commands"],
  "permissions": ["telegram.reply"],
  "files": []
}
```

| Field | Contract |
| --- | --- |
| `format` | Must be `serenox.plugin.package/v1`; the packer fills it when omitted. |
| `id` | Stable lowercase ID, 1–64 bytes, using letters, digits, `.`, `_`, or `-`; it must start and end alphanumerically. It should match RPC metadata. |
| `name` | Display name, 1–128 bytes. |
| `version` | Package version token, 1–64 bytes. Compatibility ranges use SemVer-style versions with two or three numeric components. |
| `description` | Optional, at most 4096 bytes. |
| `category` | `go-executable` or `python-sidecar`. A package has one category. |
| `protocol`, `core` | Inclusive `min` and optional exclusive `max_exclusive`. Installation always checks both. |
| `sdk`, `sidecar` | Required Python SDK and host compatibility ranges. The active interpreter must satisfy both before install and restore. |
| `entrypoints` | 1–32 canonical relative paths. Platform pairs are unique. Go entries require exact OS and architecture; Python entries may use `any` and must include explicit `module` and `class` fields. |
| `capabilities`, `permissions` | Sorted unique declaration tokens, at most 64 each. Verification validates declarations; the runtime remains responsible for granting and enforcing permissions. |
| `resources` | Optional finite queue, worker, action, storage, callback, frame, and timeout ceilings. The daemon may apply stricter limits. |
| `dependencies` | Optional lock/package metadata only. No network installation or setup code runs during verification, install, or startup. |
| `files` | Canonical payload inventory generated by `pack`: `path`, `size`, lowercase SHA-256, and mode `420` (`0644`) or `493` (`0755`). |

A portable Python package can use:

```json
{
  "format": "serenox.plugin.package/v1",
  "id": "example.legacy_echo",
  "name": "Legacy Echo",
  "version": "1.0.0",
  "category": "python-sidecar",
  "protocol": {"min": "1.0", "max_exclusive": "2.0"},
  "core": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "sdk": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "sidecar": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "entrypoints": [{"os": "any", "arch": "any", "path": "plugin.py", "module": "plugin.py", "class": "Example"}],
  "capabilities": ["commands"],
  "permissions": ["telegram.reply"],
  "files": []
}
```

Packaging does not run `go build`, `pip`, or arbitrary package hooks. The Go
binary must already target every declared platform. Python dependencies are
metadata for already-packaged, reproducible payloads; installation never
downloads code or runs dependency setup.

## Author workflow

The compiling example is in
[`examples/plugins/echo`](../../../examples/plugins/echo). From the project root:

```sh
go test ./examples/plugins/echo
rm -rf /tmp/serenox-echo-package
mkdir -p /tmp/serenox-echo-package/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o /tmp/serenox-echo-package/bin/echo \
  ./examples/plugins/echo
bin/serenox plugin pack \
  --source /tmp/serenox-echo-package \
  --manifest examples/plugins/echo/manifest.json \
  --output dist/example.echo-1.0.0.srnx
bin/serenox plugin verify dist/example.echo-1.0.0.srnx
```

Add one entrypoint and one binary for each supported OS/architecture. Use
`verify --all-platforms` to inspect a multi-platform artifact on a host for
which it has no entrypoint. That option skips only entrypoint selection;
`--skip-version-check` separately skips core/protocol range checks. `install`
always restores both checks.

## Signing and trust

Signing is optional at the file-format level. Production deployments should
select an explicit trust policy rather than relying on the CLI default.

```sh
openssl genpkey -algorithm Ed25519 -out author-ed25519.pem
bin/serenox plugin pack \
  --source /tmp/serenox-echo-package \
  --manifest examples/plugins/echo/manifest.json \
  --signing-key author-ed25519.pem \
  --output dist/example.echo-1.0.0.srnx
bin/serenox plugin verify \
  --trust trust.json \
  --trust-mode require-trusted \
  dist/example.echo-1.0.0.srnx
```

`--signing-key` accepts an Ed25519 seed/private key as raw bytes or base64, or
an Ed25519 PKCS#8 PEM. If `--key-id` is omitted, the packer derives an ID from
the first 16 bytes of the public-key SHA-256 digest.

The optional canonical `signature.json` contains format
`serenox.plugin.signature/v1`, algorithm `ed25519`, key ID, raw public key in
base64, a domain-separated manifest digest, and signature. The signature covers
the canonical manifest, which in turn commits to every payload's path, size,
mode, and SHA-256.

A trust file is strict JSON:

```json
{
  "mode": "require-trusted",
  "keys": [
    {
      "id": "ed25519:0123456789abcdef0123456789abcdef",
      "public_key": "BASE64_OF_THE_32_BYTE_ED25519_PUBLIC_KEY"
    }
  ]
}
```

Trust modes have distinct meanings:

- `allow-unsigned` accepts an unsigned archive, but still verifies any signature
  that is present;
- `require-signed` requires a valid Ed25519 signature and may use the public key
  embedded in the package;
- `require-trusted` additionally requires the signature key ID and bytes to
  match the local trust file.

The trust file must come from an administrator-controlled path. An embedded key
proves consistency, not publisher identity.

## User installation and version management

The normal user workflow is one downloaded file and one command:

```sh
bin/serenox plugin verify --trust trust.json plugin.srnx
bin/serenox plugin install --trust trust.json plugin.srnx
bin/serenox plugin install plugin.srnx  # also hot-loads when the daemon is running
bin/serenox plugin list
bin/serenox plugin rollback example.echo
bin/serenox plugin rollback example.echo 1.0.0
bin/serenox plugin uninstall --version 1.0.0 example.echo
bin/serenox plugin uninstall example.echo
```

For local development, the simplest workflow is:

```sh
cd examples/plugins/time
serenox plugin install
```

`install` discovers the project root, selects the current OS/architecture
entrypoint, runs `go build`, creates `../dist/<id>-<version>.srnx`, and installs
it into the project `data` directory. `build` is available when an
archive is needed without installation; `pack` remains available for an
already-built directory. The install root can still be overridden with
`--root`, and signed workflows can use `build --signing-key ...` or
`pack --signing-key ...` before installing the resulting archive.

The canonical command is `serenox plugin`. The separately built
`serenox-plugin` and historical `srnx-plugin` binaries execute the same command
implementation and remain available as compatibility aliases.

`install` verifies the entire open archive before extracting, selects the local
entrypoint, stages files, fsyncs them, atomically commits the version, and then
atomically updates `active.json`. Reinstalling identical bytes for the same
version is idempotent and reactivates it; reusing a version with a different
manifest digest is rejected.

The default retention is three versions including the active one; set
`--retain N` from 1 through 32. `rollback <id>` activates the most recently
installed non-active retained version, while an explicit version selects that
version. Removing an active version selects the newest remaining version; a
full uninstall removes every retained version of that plugin.

The data root is `--root`, then `SERENOX_DATA_DIR`, then `data`, matching the
daemon's default. A service deployment should set the same absolute `data_dir`
for both tools. Its relevant layout is:

```text
plugins/<id>/active.json
plugins/<id>/versions/<version>/...payload...
plugins/<id>/versions/<version>/.serenox/manifest.json
plugins/<id>/versions/<version>/.serenox/signature.json
plugins/<id>/versions/<version>/.serenox/receipt.json
```

`active.json` is an on-disk selection record. The daemon's plugin controller is
responsible for reconciling that selection with a supervised process; package
installation never executes archive content during verification or extraction.

### Hot-loading during development

When the daemon is already running, `install` automatically hot-loads the new
package instead of requiring a restart:

```sh
bin/serenox plugin pack --source ./my-plugin --output /tmp/my-plugin.srnx
bin/serenox plugin install --root ./data /tmp/my-plugin.srnx
```

The install command sends the archive path to the running daemon over
`<data_dir>/.serenox/control.sock`. The daemon rotates a mode-0600 token at
startup, verifies and installs the archive, then starts the new supervised
process in place. If an existing version is running, it is stopped only after
the new package has been verified; a failed start restores the previous active
version and process. Use `--socket PATH` with the explicit `load` command when
the daemon uses a non-default control socket. If no daemon is running,
installation still succeeds and the package is restored at the next startup;
the explicit `serenox plugin load` command remains available when a running
daemon is required. The CLI does not execute plugin code itself.

For an eventual Telegram document flow, the command handler should save the
bounded upload to a temporary file and call this same verifier/manager. It
should not invent a second “chat plugin” format. The intended interaction is
reply-to-document install, followed by the same list/rollback/uninstall
operations and trust policy used by the CLI.

## Fixed verifier limits

The v1 defaults are deliberately finite:

| Resource | Maximum |
| --- | ---: |
| compressed archive | 64 MiB |
| payload files | 256 |
| one payload file | 32 MiB |
| total declared/uncompressed content | 128 MiB |
| `manifest.json` | 256 KiB |
| `signature.json` | 32 KiB |

Verification rejects oversized input before extraction, decompression past a
declared limit, path traversal, absolute or backslash paths, drive-style roots,
non-UTF-8 names, duplicate and case-colliding paths, symlinks, directories and
special files, undeclared files, missing files, invalid modes, hash/size
mismatches, malformed or non-canonical metadata, incompatible versions, and a
missing platform entrypoint.
# Event declarations

Signed manifests carry exact `event_publications` and `event_subscriptions` arrays. Every declaration requires `events.publish` or `events.subscribe` plus `event.<operation>.<type>.v<version>`. Runtime handshake declarations must match the signed arrays exactly and cannot widen them.
