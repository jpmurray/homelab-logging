#!/usr/bin/env bash

PCT_BIN="${PCT_BIN:-pct}"

pct_exec() {
    local ctid="$1"
    shift
    "$PCT_BIN" exec "$ctid" -- "$@"
}

require_pct() {
    if [[ "$PCT_BIN" == */* ]]; then
        [[ -x "$PCT_BIN" ]] || die "pct executable not found: $PCT_BIN"
    else
        command -v "$PCT_BIN" >/dev/null 2>&1 || die "pct was not found; run this on a Proxmox VE host"
    fi
}

list_local_containers() {
    "$PCT_BIN" list | awk 'NR > 1 && $1 ~ /^[1-9][0-9]*$/ { print $1 "\t" $2 }'
}

verify_container() {
    local ctid="$1" status
    [[ "$ctid" =~ ^[1-9][0-9]*$ ]] || die "CTID must be a positive integer"
    "$PCT_BIN" status "$ctid" >/dev/null 2>&1 || die "Container $ctid does not exist"
    status="$("$PCT_BIN" status "$ctid" | awk '{print $2}')"
    [[ "$status" == "running" ]] || die "Container $ctid is not running (status: $status)"
}

remote_file_exists() {
    pct_exec "$1" test -f "$2" >/dev/null 2>&1
}

pull_remote_file() {
    "$PCT_BIN" pull "$1" "$2" "$3"
}

push_remote_file() {
    "$PCT_BIN" push "$1" "$2" "$3" --perms 0644
}

ensure_rsyslog() {
    local ctid="$1"
    if pct_exec "$ctid" sh -c 'command -v rsyslogd >/dev/null 2>&1'; then
        return 0
    fi
    info "Installing rsyslog in CT $ctid"
    pct_exec "$ctid" apt-get update
    pct_exec "$ctid" env DEBIAN_FRONTEND=noninteractive apt-get install -y rsyslog
}

source_path_exists() {
    local ctid="$1" pattern="$2"
    pct_exec "$ctid" bash -c 'compgen -G "$1" | grep -q .' _ "$pattern" >/dev/null 2>&1
}

alloy_reachable() {
    local ctid="$1" host="$2" port="$3"
    pct_exec "$ctid" bash -c 'exec 3<>/dev/tcp/$1/$2' _ "$host" "$port" >/dev/null 2>&1
}
