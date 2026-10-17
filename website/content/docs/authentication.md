---
title: Telegram authorization
description: Authorize a real account while protecting API credentials and session data.
---

# Telegram account authorization

## Current verification boundary

The native phone authorization path is wired in source:

- the canonical `serenox login` command invokes `cmd/serenox/login.go` directly;
- the command loads `internal/config.Telegram`, accepts `--config` and the
  one-shot `--phone` override, and supplies bounded terminal prompts;
- `internal/telegram.Authorize` uses pinned `github.com/gotd/td` APIs for an
  existing user session or phone, code, and optional 2FA authorization;
- `internal/telegram.FileSession` supplies owner-only atomic session storage;
- the daemon has a noninteractive live branch which verifies the saved identity
  before creating `LiveTransport`.

A local source/API audit, the gotd-enabled package compile, the Go test suite,
the race suite, and the release-shaped build pass in the current checkout. A
real-account network authorization fixture has not been run as part of ordinary
CI: it is an explicit operator gate because it needs disposable credentials and
an account. Consequently, this document distinguishes compiled/tested behavior
from behavior exercised against Telegram's network.

The fail-closed [`scripts/test-auth-boundary.sh`](../../../scripts/test-auth-boundary.sh)
check remains useful on a constrained host: it tests the CLI, bootstrap mapping,
and bounded session behavior in an isolated copy whose concrete gotd files are
deliberately removed. It complements, rather than replaces, the native gotd
compile and the opt-in real-account gate.

Native update reception and the plain/HTML send, reply, edit, and delete mapping
are present in source. Outbound resolution uses a capacity-bounded peer cache
and never invents a user/channel access hash. Callback/URL/switch buttons on
send, reply, and edit actions are mapped to Telegram inline markup. Inline
answers, client operations, unknown parse modes, and unresolved peers fail with
explicit errors.
These action paths share the compiled adapter boundary above; only the
real-account network acceptance remains unverified by default.

## Phone authorization UX

The implemented invocation is:

```sh
serenox login --config config.json
```

One invocation performs at most one authorization flow:

1. The command loads defaults, the JSON file, `SERENOX_TELEGRAM_*` environment
   values, and typed CLI overrides using the same precedence as the daemon. A
   missing API credential is temporarily allowed so the interactive prompt can
   collect it.
2. It asks for a missing API ID and API hash, validates the complete pair,
   `auth_mode=phone`, metadata, locale, and resolved session path before
   constructing the gotd client. The API hash is hidden on a terminal and
   neither credential is written to JSON.
3. If the session is already authorized, gotd reuses it without asking for a
   phone, code, or password and Serenox verifies the account identity.
4. Otherwise Serenox gets the phone from `SERENOX_TELEGRAM_PHONE`, `--phone`, or
   the `Phone (E.164)` prompt. Spaces, dashes, parentheses, a leading `+`, and
   the international `00` prefix are normalized; a JSON `phone` field is
   rejected. A number without `+` must already include its country code.
5. Serenox requests the code once and displays gotd's non-secret delivery type
   and expected length when available. The input is limited to 4096 bytes.
6. If Telegram requires 2FA, Serenox reads the existing account password with
   terminal echo disabled. A non-terminal input cannot supply a 2FA password.
7. After gotd reports authorization, Serenox verifies `Auth().Status` and
   `Self`, then prints only user ID, optional username, and session path.

There is no Serenox resend or retry loop. An invalid/expired code, flood error,
network failure, or wrong password ends that invocation; the operator may run
the command again after addressing the reported error. This is intentionally
not advertised as an automatic retry facility.

`serenox` installs an interrupt-aware context for `SIGINT`/`SIGTERM`. Phone,
code, and 2FA values are not added to long-lived application state or printed by
Serenox. `FileSession` makes each individual gotd session write atomic; the
whole multi-request authorization exchange is not presented as one filesystem
transaction.

## API credentials and configuration

The local configuration type has these fields:

```json
{
  "telegram": {
    "enabled": true,
    "api_id": 123456,
    "api_hash": "0123456789abcdef0123456789abcdef",
    "session_file": "telegram.session.json",
    "auth_mode": "phone",
    "device_model": "Serenox",
    "app_version": "0.1.0",
    "system_version": "Go",
    "lang_code": "en",
    "system_lang_code": "en-US"
  }
}
```

`api_id` is a positive 32-bit Telegram application ID. `api_hash` is validated
as exactly 32 hexadecimal characters and is treated as sensitive. They must be
set together after interactive collection. Both may be supplied through
environment instead of JSON:

```sh
export SERENOX_TELEGRAM_ENABLED=true
export SERENOX_TELEGRAM_API_ID=123456
export SERENOX_TELEGRAM_API_HASH='<api-hash>'
export SERENOX_TELEGRAM_PHONE=+15551234567  # optional after first login
serenox login --config config.json
```

Login itself only requires the API credential pair and creates/verifies the
session used by the next `serenox run`. The `telegram.enabled` field is kept
for configuration compatibility; live mode is selected by `serenox run`, while
`-recorded` explicitly selects the offline development adapter.

When the pair is absent, the command prompts for it. The non-secret ID may also
be supplied as `--api-id 123456`; there is intentionally no API-hash flag so a
secret is not placed in shell history. A non-TTY stdin may provide the hash as
an input line for scripted use, while a terminal reads it without echo.

After a successful login, the pair is stored in the owner-readable-only
`data/telegram.credentials.json` file (or the configured `data_dir`) so the
next `serenox run` does not ask for it again. It is not written to the main
JSON configuration. Environment and explicit JSON values still take
precedence when supplied.

The remaining implemented overrides are
`SERENOX_TELEGRAM_SESSION_FILE`, `SERENOX_TELEGRAM_AUTH_MODE`,
`SERENOX_TELEGRAM_DEVICE_MODEL`, `SERENOX_TELEGRAM_APP_VERSION`,
`SERENOX_TELEGRAM_SYSTEM_VERSION`, `SERENOX_TELEGRAM_LANG_CODE`, and
`SERENOX_TELEGRAM_SYSTEM_LANG_CODE`. Login codes and 2FA passwords are terminal
inputs, not configuration fields. Shell history, process managers, and CI must
not echo credential-bearing environment assignments.

## Session ownership and daemon startup

`Runtime.TelegramSessionPath()` resolves a relative `session_file` beneath
`data_dir`; relative traversal outside that directory is rejected. The default
path is `data/telegram.session.json`. Bootstrap caps the opaque gotd session at
4 MiB.

`FileSession.Save` creates a missing parent directory with mode `0700`, creates
the replacement in that directory, applies mode `0600`, writes and syncs it,
closes it, atomically renames it over the destination, and syncs the directory.
On platforms where POSIX modes do not enforce access, a production port still
needs an equivalent owner-only ACL.

With no `-recorded` fixture, the native daemon flow is:

```sh
serenox run --config config.json
```

The daemon builds the same session config and calls `Authorize` with a nil
prompt. It can only reuse an already-authorized
session: it never reads phone, code, or 2FA values from daemon stdin, and it
clears any environment-provided phone from its runtime configuration before the
long-running application starts. It derives the command-owner ID from the
verified Telegram identity. If `--self-id` is also supplied, it must match that
identity.

A missing or unauthorized session returns a nonzero startup error directing the
operator to `serenox login --config config.json`. The daemon then constructs the
gotd live transport. The code is compiled and adapter-tested, while network
acceptance is covered only by the explicit smoke-test gate.

Use one session file per account and do not point concurrent daemon processes at
the same file. Multi-account orchestration and cross-process session locking are
not part of the verified contract.

## Unsupported authorization and live paths

| Path | Current result |
|---|---|
| Native network login | Wired, compiled, and adapter-tested; real-account network fixture is an explicit opt-in gate. |
| Automatic code resend/retry | Unsupported. One authorization attempt is made per CLI invocation. |
| QR login | Unsupported. `auth_mode` accepts only `phone`; `qr` fails configuration validation. |
| Passkeys | Unsupported. There is no passkey/WebAuthn DTO, prompt, or transition. |
| New-account sign-up | Unsupported. gotd calls `SignUp`, which returns `ErrSignUpUnsupported`; create the account in an official client. |
| Terms acceptance during sign-in | Unsupported. It returns `ErrTermsUnsupported` and directs completion in an official client. |
| 2FA recovery/reset | Unsupported. Only submission of the existing account password is implemented. |
| Bot-token login | Unsupported by this user-session flow; inline Bot API credentials are separate. |
| Telegram Test DC selection | Not represented in current configuration or `serenox login`. |
| Native reply/edit/send/delete | Compiled and adapter-tested for plain/HTML text with bounded peer resolution; real-account acceptance is opt-in. |
| Native inline answers and client operations | Unsupported with `ErrActionUnsupported` or `ErrUnsupportedActionOptions`; callback/URL/switch buttons on send/reply/edit are supported. |

No unsupported path reports that an account or action succeeded.

## Telegram test accounts

Exercise authorization against Telegram Test DCs after an explicit test-DC
option is implemented. Telegram documents reserved numbers as `99966XYYYY`,
where `X` is Test DC `1` through `3` and `YYYY` varies; the confirmation code is
the DC digit repeated five times. These numbers work only on Test DCs. Their
data is unsafe for private information and is periodically wiped. See
[Telegram User Authorization — Test Accounts](https://core.telegram.org/api/auth#test-accounts).

The current configuration has no Test DC selector and Serenox does not implement
sign-up, so the present source cannot provision these accounts. Recorded-update
and authorization fixtures remain the credential-free local test path.

## Redaction, revocation, and rotation

`config.Telegram.String`, `GoString`, and the corresponding `Runtime` formatters
replace the API hash and phone with redaction markers. Validation errors name
the field and rule without echoing its rejected value. Raw JSON, environment,
and session content are still sensitive: keep them out of logs, crash reports,
fixtures, source control, plugin RPC, and support bundles.

If an API credential or session may have leaked:

1. Stop every daemon using that account.
2. Revoke the affected authorization from Telegram's official active-session
   controls, then remove the local session after the daemon has stopped.
3. Replace or revoke the Telegram application credential through Telegram's
   developer tools where available and update the protected secret source.
4. Run `serenox login --config config.json`, verify the printed identity, and
   only then restart the daemon.

Changing only `api_hash` does not sanitize an exported session. Session
revocation and fresh authorization are separate operations.
