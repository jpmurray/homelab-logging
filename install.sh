#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${HLL_REPOSITORY:-jpmurray/homelab-logging}"
INSTALL_ROOT="${HLL_INSTALL_ROOT:-/opt/homelab-logging}"
BIN_DIR="${HLL_BIN_DIR:-/usr/local/bin}"
GITHUB_URL="${HLL_GITHUB_URL:-https://github.com}"
REQUESTED_VERSION="latest"

usage() {
    cat <<'EOF'
Install or update homelab-logging on a Proxmox node.

Usage: install.sh [--version VERSION]

Options:
  --version VERSION  Install a specific release instead of the latest release
  -h, --help         Show this help

The site configuration is kept at /opt/homelab-logging/config.json and is not
overwritten by updates. Set HLL_INSTALL_ROOT or HLL_BIN_DIR to override paths.
EOF
}

fail() {
    printf 'homelab-logging installer: %s\n' "$*" >&2
    exit 1
}

while (($#)); do
    case "$1" in
        --version)
            (($# >= 2)) || fail "--version requires a value"
            REQUESTED_VERSION="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown option: $1"
            ;;
    esac
done

for command in curl unzip; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

if [[ "$REQUESTED_VERSION" == latest ]]; then
    release_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$GITHUB_URL/$REPOSITORY/releases/latest")" || \
        fail "could not determine the latest release"
    version="${release_url%/}"
    version="${version##*/}"
else
    version="$REQUESTED_VERSION"
fi

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "invalid release version: $version"

release_name="homelab-logging-$version"
release_dir="$INSTALL_ROOT/releases/$release_name"
current_link="$INSTALL_ROOT/current"
config_path="$INSTALL_ROOT/config.json"
archive_name="$release_name-linux-amd64.zip"
archive_url="$GITHUB_URL/$REPOSITORY/releases/download/$version/$archive_name"

mkdir -p "$INSTALL_ROOT/releases" "$BIN_DIR"

lock_dir="$INSTALL_ROOT/.install-lock"
if ! mkdir "$lock_dir" 2>/dev/null; then
    fail "another installation is already running ($lock_dir exists)"
fi

work_dir=""
cleanup() {
    [[ -z "$work_dir" ]] || rm -rf -- "$work_dir"
    rmdir "$lock_dir" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ ! -d "$release_dir" ]]; then
    work_dir="$(mktemp -d "$INSTALL_ROOT/.install.XXXXXX")"
    printf 'Downloading homelab-logging %s...\n' "$version"
    curl -fL --retry 3 -o "$work_dir/$archive_name" "$archive_url"
    unzip -q "$work_dir/$archive_name" -d "$work_dir/unpacked"

    unpacked="$work_dir/unpacked/$release_name"
    [[ -x "$unpacked/homelab-logging" ]] || fail "release archive does not contain an executable"
    [[ -f "$unpacked/VERSION" ]] || fail "release archive does not contain VERSION"
    [[ "$(tr -d '[:space:]' < "$unpacked/VERSION")" == "$version" ]] || \
        fail "release archive version does not match $version"

    mv "$unpacked" "$release_dir"
fi

if [[ ! -f "$config_path" ]]; then
    if [[ -f "$current_link/config.json" ]]; then
        cp -L "$current_link/config.json" "$config_path"
        printf 'Preserved existing site configuration at %s\n' "$config_path"
    else
        cp "$release_dir/config.json" "$config_path"
        printf 'Created site configuration at %s\n' "$config_path"
    fi
fi

rm -f "$release_dir/config.json"
ln -s ../../config.json "$release_dir/config.json"

"$release_dir/homelab-logging" \
    --config "$config_path" \
    --profiles-dir "$release_dir/services" \
    --validate >/dev/null

ln -sfn "$release_dir" "$current_link"
ln -sfn "$current_link/homelab-logging" "$BIN_DIR/homelab-logging"
if [[ ! -f "$INSTALL_ROOT/install.sh" ]] || ! cmp -s "$0" "$INSTALL_ROOT/install.sh"; then
    install -m 0755 "$0" "$INSTALL_ROOT/install.sh"
fi
ln -sfn "$INSTALL_ROOT/install.sh" "$BIN_DIR/homelab-logging-update"

printf 'homelab-logging %s is installed.\n' "$version"
printf 'Configuration: %s\n' "$config_path"
printf 'Run: homelab-logging --sync --dry-run\n'
