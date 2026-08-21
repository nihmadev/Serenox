#!/usr/bin/env sh
set -eu

# Download the released Serenox binaries without requiring a package manager.
# Usage: curl -fsSL https://raw.githubusercontent.com/nihmadev/Serenox/main/scripts/install.sh | sh

die() {
    printf 'serenox installer: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Usage: install.sh [--version TAG] [--install-dir DIR] [--no-plugin]

Environment overrides are also supported:
  SERENOX_VERSION, SERENOX_INSTALL_DIR, SERENOX_INSTALL_PLUGIN=0
  SERENOX_REPO, SERENOX_RELEASE_BASE_URL
EOF
}

version=${SERENOX_VERSION:-latest}
install_dir=${SERENOX_INSTALL_DIR:-${HOME:-}/.local/bin}
repo=${SERENOX_REPO:-nihmadev/Serenox}
base_url=${SERENOX_RELEASE_BASE_URL:-https://github.com/${repo}/releases}
install_plugin=${SERENOX_INSTALL_PLUGIN:-1}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || die "--version requires a tag"
            version=$2
            shift 2
            ;;
        --install-dir)
            [ "$#" -ge 2 ] || die "--install-dir requires a directory"
            install_dir=$2
            shift 2
            ;;
        --no-plugin)
            install_plugin=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
done

[ -n "$install_dir" ] || die "install directory is empty"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported operating system: $(uname -s)" ;;
esac
case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
esac

archive_name="serenox_${os}_${arch}.tar.gz"
case "$version" in
    latest) release_url="${base_url%/}/latest/download/${archive_name}" ;;
    *) release_url="${base_url%/}/download/${version}/${archive_name}" ;;
esac
if [ "$version" = latest ]; then
    checksum_url="${base_url%/}/latest/download/checksums.txt"
else
    checksum_url="${base_url%/}/download/${version}/checksums.txt"
fi

download() {
    url=$1
    destination=$2
    case "$url" in
        https://*) curl -fsSL --retry 3 --retry-delay 1 --proto '=https' --tlsv1.2 -o "$destination" "$url" ;;
        file://*) curl -fsSL -o "$destination" "$url" ;;
        *) die "release URL must use https:// (or explicit file:// test source): $url" ;;
    esac
}

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/serenox-install.XXXXXX") || die "cannot create temporary directory"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT HUP INT TERM

archive="$tmp_dir/$archive_name"
checksums="$tmp_dir/checksums.txt"
download "$release_url" "$archive" || die "download failed: $release_url"
download "$checksum_url" "$checksums" || die "checksum download failed: $checksum_url"

expected=$(awk -v file="$archive_name" '$NF == file { print $1; exit }' "$checksums")
[ "$expected" ] || die "checksums.txt does not contain $archive_name"
case "$expected" in *[!0-9a-fA-F]*) die "invalid checksum for $archive_name" ;; esac
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive" | awk '{print $1}')
else
    die "sha256sum or shasum is required"
fi
[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ] || die "checksum mismatch for $archive_name"

tar_list="$tmp_dir/archive.list"
tar -tzf "$archive" > "$tar_list"
tar -tvzf "$archive" > "$tmp_dir/archive.verbose.list"
while IFS= read -r entry; do
    case "$entry" in
        /*|../*|*/../*|..|*/..)
            die "archive contains an unsafe path: $entry" ;;
    esac
done < "$tar_list"
while IFS= read -r entry; do
    type=${entry%${entry#?}}
    case "$type" in
        -|d) ;;
        *) die "archive contains a non-regular entry" ;;
    esac
done < "$tmp_dir/archive.verbose.list"

extract_dir="$tmp_dir/extracted"
mkdir -p "$extract_dir"
tar -xzf "$archive" -C "$extract_dir"

require_file() {
    [ -f "$extract_dir/$1" ] || die "release archive is missing $1"
}
require_file serenox
require_file serenoxd
[ "$install_plugin" = 0 ] || require_file serenox-plugin

mkdir -p "$install_dir"
install_one() {
    name=$1
    destination="$install_dir/$name"
    temporary="$destination.tmp.$$"
    rm -f "$temporary"
    install -m 0755 "$extract_dir/$name" "$temporary"
    mv -f "$temporary" "$destination"
    printf 'installed %s\n' "$destination"
}
install_one serenox
install_one serenoxd
[ "$install_plugin" = 0 ] || install_one serenox-plugin

printf 'Serenox %s installed for %s/%s\n' "$version" "$os" "$arch"
case ":${PATH:-}:" in
    *:"$install_dir":*) ;;
    *) printf 'Add %s to PATH to invoke serenox directly.\n' "$install_dir" ;;
esac
