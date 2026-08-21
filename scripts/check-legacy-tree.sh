#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected="9e8d0f1a21eb0d004fd83e5714e9d07d9be250d332671952dc05d2f44ffada45"
actual="$(cd "$root" && find hikka -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"

if [[ "$actual" != "$expected" ]]; then
  printf 'legacy tree hash mismatch: expected=%s actual=%s\n' "$expected" "$actual" >&2
  exit 1
fi

printf 'legacy tree hash OK: %s\n' "$actual"
