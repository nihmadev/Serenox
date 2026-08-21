#!/usr/bin/env bash
set -euo pipefail
target="${1:-/home/nihmadev/Serenox/GO_REWRITE_AGENT_PROMPT.md}"
expected="2bd1b56e42f924ab7ccbc12e83a7748611fac1204ed69a5800cebdfaa54d7dc2"
if [[ ! -e "$target" ]]; then
  printf 'ALREADY_ABSENT:%s\n' "$target"
  exit 0
fi
actual=$(sha256sum "$target" | awk '{print $1}')
if [[ "$actual" != "$expected" ]]; then
  printf 'HASH_MISMATCH:%s\n' "$target" >&2
  exit 2
fi
rm -- "$target"
printf 'REMOVED:%s\n' "$target"
