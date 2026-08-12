#!/usr/bin/env bash

HLL_VERSION="1.3.0"

info() { printf '==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

validate_site_config() {
    local file="$1"
    jq -e '
        . as $config |
        type == "object" and
        ((keys - ["schema_version","cluster","location","origin_role","alloy","remote_config","legacy_configs"]) | length == 0) and
        .schema_version == 1 and
        (.cluster | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$")) and
        (.location | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$")) and
        (.origin_role | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$")) and
        (.alloy | type == "object") and
        ((.alloy | keys) - ["host","port","protocol"] | length == 0) and
        (.alloy.host | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9_.:-]{0,252}$")) and
        (.alloy.port | type == "number" and floor == . and . >= 1 and . <= 65535) and
        (.alloy.protocol == "tcp") and
        (.remote_config | type == "string" and test("^/etc/rsyslog[.]d/[A-Za-z0-9._-]+[.]conf$")) and
        (.legacy_configs | type == "array") and
        (.legacy_configs | length == (unique | length)) and
        (all(.legacy_configs[];
            type == "string" and
            test("^/etc/rsyslog[.]d/[A-Za-z0-9._-]+[.]conf$") and
            . != $config.remote_config
        ))
    ' "$file" >/dev/null 2>&1 || die "Invalid site configuration: $file"
}

make_temp_file() { mktemp "${TMPDIR:-/tmp}/homelab-logging.XXXXXX"; }

cleanup_files=()
register_cleanup() { cleanup_files+=("$1"); }

cleanup() {
    local path
    for path in "${cleanup_files[@]:-}"; do
        [[ -n "$path" ]] || continue
        rm -f "$path"
    done
    return 0
}

acquire_lock() {
    local ctid="$1"
    if [[ "${HLL_TESTING:-0}" == "1" ]]; then
        LOCK_DIR="${TMPDIR:-/tmp}/homelab-logging-${ctid}.lock"
    else
        LOCK_DIR="/run/lock/homelab-logging-${ctid}.lock"
    fi
    if ! mkdir "$LOCK_DIR" 2>/dev/null; then
        die "Another homelab-logging operation is active for CT $ctid"
    fi
}

release_lock() {
    if [[ -n "${LOCK_DIR:-}" ]]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
        LOCK_DIR=""
    fi
    return 0
}

require_root_for_write() {
    if [[ "${EUID:-$(id -u)}" -ne 0 && "${HLL_TESTING:-0}" != "1" ]]; then
        die "Run this command as root on a Proxmox VE host"
    fi
}

proxmox_node_name() {
    local node="${HLL_NODE_OVERRIDE:-}"
    if [[ -z "$node" ]]; then
        node="$(hostname -s)"
    fi
    [[ "$node" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$ ]] || die "Invalid Proxmox node name: $node"
    printf '%s\n' "$node"
}

print_hr() { printf '%s\n' '----------------------------------------------------------------'; }
