#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TARGET=${1:-$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)}

python3 - "$TARGET" <<'PY'
from pathlib import Path
import shutil
import sys

root = Path(sys.argv[1]).resolve()
for relative in ("src", "tests"):
    candidate = root / relative
    if candidate.exists():
        shutil.rmtree(candidate)
PY
tar -xzf "$SCRIPT_DIR/original-sidecar.tar.gz" -C "$TARGET"
printf 'restored baseline sidecar at %s\n' "$TARGET"
