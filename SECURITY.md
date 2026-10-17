# Security Policy

## Supported versions

Security fixes are provided for the latest released version and for the
default branch. Older releases may not receive security updates.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through
[GitHub Security Advisories](https://github.com/nihmadev/Serenox/security/advisories/new).
Do not open a public issue or disclose the vulnerability publicly before the
maintainers have had an opportunity to investigate and publish a fix.

Include, when available:

- the affected version or commit;
- a concise description of the impact;
- reproducible steps or a minimal proof of concept;
- any relevant logs, traces, or suggested mitigation.

The maintainers will acknowledge a report as soon as practical, keep the
reporter informed during triage, and coordinate the disclosure timeline and
credit with the reporter. Please do not include credentials, session files,
API hashes, or other personal data in a report.

If private reporting is unavailable in your GitHub account, contact the
repository maintainers through their GitHub profiles and request a private
security contact.
# Event Fabric boundary

Event source/account/attempt/hop identity is host-assigned. Exact signed declarations and permissions prevent subscription or publication widening. Payloads and diagnostics are bounded; payloads, user/chat IDs, event IDs, traces, dedupe keys, URLs, and error text must never become metric labels. Durable at-least-once delivery can repeat plugin effects, so handlers must use durable idempotency and treat ambiguous external mutations as terminal until explicitly reconciled.
