#!/bin/sh
set -eu
target=${1:?usage: ROLLBACK.sh TARGET_COPY_ROOT}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
destination="$target/go.mod"
expected_modified=9877eaa65e4009ff051be83b62af1589d476e43f46a9ad0219971a002ca0df63
expected_baseline=19e8b3da714022160890a487565adc3676b34e54caa5f8ac003dbb6c63655f0b
actual=$(sha256sum "$destination" | awk '{print $1}')
if [ "$actual" != "$expected_modified" ]; then
  printf 'rollback refused: modified hash mismatch: %s\n' "$actual" >&2
  exit 2
fi
install -m 0644 "$script_dir/BASELINE_FILE" "$destination"
restored=$(sha256sum "$destination" | awk '{print $1}')
if [ "$restored" != "$expected_baseline" ]; then
  printf 'rollback failed: restored hash mismatch: %s\n' "$restored" >&2
  exit 1
fi
printf 'rollback restored: go.mod sha256=%s module=github.com/serenox/serenox-go\n' "$restored"
