#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BUILD_TMP_DIR="${SERENOX_BUILD_TMP_DIR:-${ROOT_DIR}/.cache/build-tmp}"
GO_CACHE_DIR="${SERENOX_GO_CACHE_DIR:-${ROOT_DIR}/.cache/go-build}"

mkdir -p "${BUILD_TMP_DIR}" "${GO_CACHE_DIR}"

# Keep large Go compile/link temporary files and the build cache on the
# workspace filesystem. The system /tmp can be too small for the Telegram
# dependency tree.
export TMPDIR="${BUILD_TMP_DIR}"
export GOCACHE="${GO_CACHE_DIR}"

cd "${ROOT_DIR}"
exec make build
