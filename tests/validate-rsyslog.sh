#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
command -v rsyslogd >/dev/null 2>&1 || {
    printf 'SKIP: rsyslogd is not installed\n'
    exit 0
}

# shellcheck source=lib/common.sh
source "$ROOT/lib/common.sh"
# shellcheck source=lib/rsyslog.sh
source "$ROOT/lib/rsyslog.sh"

TEST_TMP="$(mktemp -d "${TMPDIR:-/tmp}/homelab-logging-rsyslog.XXXXXX")"
trap 'rm -rf "$TEST_TMP"' EXIT
: > "$TEST_TMP/docker-sources"

for profile in "$ROOT"/services/*.json; do
    candidate="$TEST_TMP/$(basename "$profile")"
    {
        printf 'global(workDirectory="%s")\n' "$TEST_TMP"
        generate_rsyslog_config "$ROOT/config.json" "$profile" "$TEST_TMP/docker-sources" "test-node"
    } > "$candidate"
    rsyslogd -N1 -f "$candidate" >/dev/null
done

printf 'Validated %s generated rsyslog configurations\n' "$(find "$ROOT/services" -maxdepth 1 -name '*.json' | wc -l | tr -d ' ')"
