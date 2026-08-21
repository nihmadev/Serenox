#!/usr/bin/env bash
set -euo pipefail

# This is deliberately a plugin/package/RPC test. It never imports, compiles,
# or contacts the concrete Telegram/gotd adapter.

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_PARENT="${TMPDIR:-/tmp}"
TEMP_DIR="$(mktemp -d "${TMP_PARENT%/}/serenox-time-plugin.XXXXXX")"

cleanup() {
	case "$TEMP_DIR" in
		"${TMP_PARENT%/}"/serenox-time-plugin.*) rm -rf -- "$TEMP_DIR" ;;
		*) printf 'refusing to remove unexpected temporary path: %s\n' "$TEMP_DIR" >&2 ;;
	esac
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

export GOTOOLCHAIN=local
export GOCACHE="$ROOT_DIR/.cache/go-build"
export GOMAXPROCS=1
export GOTRACEBACK=none
export GOMEMLIMIT="512MiB"
export GOGC=50

mkdir -p -- "$TEMP_DIR/payload/bin" "$TEMP_DIR/data"

cd "$ROOT_DIR"
printf '%s\n' 'BOUNDARY: Telegram/gotd is not imported, compiled, or contacted.'
printf '%s\n' 'STEP unit: go test -p=1 ./examples/plugins/time'
go test -p=1 ./examples/plugins/time

printf '%s\n' 'STEP build: bounded native plugin and E2E driver builds'
CGO_ENABLED=0 go build -p=1 -trimpath -ldflags='-s -w -buildid=' \
	-o "$TEMP_DIR/payload/bin/time-plugin" ./examples/plugins/time
CGO_ENABLED=0 go build -p=1 -trimpath -ldflags='-s -w -buildid=' \
	-o "$TEMP_DIR/time-plugin-e2e" ./scripts/time-plugin-e2e

printf '%s\n' 'STEP e2e: canonical serenox plugin engine -> .srnx verify/install -> supervisor RPC -> Dispatch'
"$TEMP_DIR/time-plugin-e2e" \
	--source "$TEMP_DIR/payload" \
	--manifest "$ROOT_DIR/examples/plugins/time/manifest.json" \
	--archive "$TEMP_DIR/example.time-1.0.0.srnx" \
	--data "$TEMP_DIR/data"
