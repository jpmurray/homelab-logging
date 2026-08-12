#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_TMP="$(mktemp -d "${TMPDIR:-/tmp}/homelab-logging-tests.XXXXXX")"
trap 'rm -rf "$TEST_TMP"' EXIT

export MOCK_CONTAINER_ROOT="$TEST_TMP/container"
export MOCK_PCT_LOG="$TEST_TMP/pct.log"
export PCT_BIN="$ROOT/tests/mock-pct.sh"
export HLL_TESTING=1
export HLL_NODE_OVERRIDE="patate-du-cluster"
export TMPDIR="$TEST_TMP"
mkdir -p "$MOCK_CONTAINER_ROOT/etc/rsyslog.d"
LEGACY="$MOCK_CONTAINER_ROOT/etc/rsyslog.d/90-alloy.conf"
printf 'legacy forwarding config\n' > "$LEGACY"

pass=0
fail=0

ok() {
    pass=$((pass + 1))
    printf 'ok %s - %s\n' "$pass" "$1"
}

not_ok() {
    fail=$((fail + 1))
    printf 'not ok - %s\n' "$1" >&2
}

assert_contains() {
    local file="$1" pattern="$2" name="$3"
    if grep -Fq -- "$pattern" "$file"; then ok "$name"; else not_ok "$name"; fi
}

assert_not_contains() {
    local file="$1" pattern="$2" name="$3"
    if grep -Fq -- "$pattern" "$file"; then not_ok "$name"; else ok "$name"; fi
}

if bash -n "$ROOT/install.sh" "$ROOT"/lib/*.sh "$ROOT/tests/mock-pct.sh"; then ok "shell syntax"; else not_ok "shell syntax"; fi
if "$ROOT/install.sh" --validate > "$TEST_TMP/validate.out"; then ok "all profiles validate"; else not_ok "all profiles validate"; fi
assert_contains "$ROOT/alloy/syslog-labels.alloy" "forward_to = []" "Alloy relabel component has its required forward_to attribute"
assert_contains "$ROOT/alloy/syslog-labels.alloy" "loki.write.local.receiver" "Alloy syslog source targets the configured local writer"
assert_contains "$ROOT/alloy/config.alloy" 'loki.source.journal "local"' "complete Alloy config preserves local journal collection"
assert_contains "$ROOT/alloy/config.alloy" 'loki.write "local"' "complete Alloy config defines the local Loki writer"
assert_contains "$ROOT/alloy/config.alloy" "label_structured_data  = true" "complete Alloy config enables structured-data labels"
assert_not_contains "$ROOT/alloy/config.alloy" "loki.write.default.receiver" "complete Alloy config has no placeholder writer"
if jq -e '
    .title == "Saint-Cluster Logging Overview" and
    .__inputs[0].name == "DS_LOKI" and
    (.panels | length) == 9 and
    (all(.panels[].targets[]?; .expr | contains("cluster=\"saint-cluster\""))) and
    ((.panels[] | select(.id == 8)).type == "table") and
    ([.templating.list[].name] == ["location","cluster","node","host","role","job","service","search"])
' "$ROOT/grafana/dashboards/saint-cluster-logging.json" >/dev/null; then
    ok "Grafana dashboard import JSON is valid"
else
    not_ok "Grafana dashboard import JSON is valid"
fi

export MOCK_SERVICES="beszel-hub.service"
if "$ROOT/install.sh" 107 --dry-run > "$TEST_TMP/beszel.conf"; then ok "Beszel hub auto-detection dry run"; else not_ok "Beszel hub auto-detection dry run"; fi
assert_contains "$TEST_TMP/beszel.conf" "# homelab-logging-profile: beszel" "Beszel hub service is auto-detected"

mkdir -p "$MOCK_CONTAINER_ROOT/var/log/postgresql"
: > "$MOCK_CONTAINER_ROOT/var/log/postgresql/postgresql-17-main.log"
export MOCK_SERVICES="postgresql.service"
if "$ROOT/install.sh" 105 --dry-run > "$TEST_TMP/postgres.conf"; then ok "auto-detection dry run"; else not_ok "auto-detection dry run"; fi
assert_contains "$TEST_TMP/postgres.conf" "Detected profile: postgres" "PostgreSQL is auto-detected"
assert_contains "$TEST_TMP/postgres.conf" 'homelab@32473 cluster=\"saint-cluster\" location=\"home\" role=\"lxc\" node=\"patate-du-cluster\" job=\"syslog\"' "origin and node labels are transmitted"
assert_contains "$TEST_TMP/postgres.conf" "/var/log/postgresql/postgresql-*-main.log" "PostgreSQL file input is generated"
assert_contains "$TEST_TMP/postgres.conf" "Would back up and deactivate legacy configuration: /etc/rsyslog.d/90-alloy.conf" "dry run reports legacy migration"

mkdir -p "$MOCK_CONTAINER_ROOT/var/log/proxmox-backup/api" "$MOCK_CONTAINER_ROOT/var/log/proxmox-backup/tasks/00"
if "$ROOT/install.sh" 100 pbs --dry-run > "$TEST_TMP/pbs.conf"; then ok "PBS dry run"; else not_ok "PBS dry run"; fi
assert_contains "$TEST_TMP/pbs.conf" ":garbage_collection:*" "PBS selected task source is present"
assert_contains "$TEST_TMP/pbs.conf" 'property(name="$.task_id")' "PBS filename correlation is preserved in the message"
assert_contains "$TEST_TMP/pbs.conf" 're_extract($!metadata!filename, "([^/]+)\$"' "PBS task regex escapes the RainerScript dollar sign"
assert_not_contains "$TEST_TMP/pbs.conf" 'File="/var/log/proxmox-backup/tasks/*/UPID:*:verificationjob:*"' "PBS verification jobs are excluded from generated config"

mkdir -p "$MOCK_CONTAINER_ROOT/var/lib/docker/containers/abc"
: > "$MOCK_CONTAINER_ROOT/var/lib/docker/containers/abc/abc-json.log"
export MOCK_DOCKER_SOURCES=$'/mealie|/var/lib/docker/containers/abc/abc-json.log\n'
if "$ROOT/install.sh" 116 docker --dry-run > "$TEST_TMP/docker.conf"; then ok "Docker dry run"; else not_ok "Docker dry run"; fi
assert_contains "$TEST_TMP/docker.conf" "Tag=\"mealie:\"" "Docker container name becomes service"
assert_contains "$TEST_TMP/docker.conf" 'job=\"docker\"' "Docker records carry the docker job"

unset MOCK_DOCKER_SOURCES
export MOCK_SERVICES="postgresql.service"
if "$ROOT/install.sh" 105 postgres > "$TEST_TMP/install.out"; then ok "mock deployment"; else not_ok "mock deployment"; fi
MANAGED="$MOCK_CONTAINER_ROOT/etc/rsyslog.d/90-homelab-alloy.conf"
if [[ -f "$MANAGED" ]]; then ok "managed config was pushed"; else not_ok "managed config was pushed"; fi
assert_contains "$MANAGED" "# homelab-logging-profile-revision: 1" "deployment records the profile revision"
if [[ ! -f "$LEGACY" ]]; then ok "legacy config was deactivated"; else not_ok "legacy config was deactivated"; fi
LEGACY_DISABLED="$(compgen -G "$LEGACY.disabled.*" | head -1 || true)"
if [[ -n "$LEGACY_DISABLED" && "$(< "$LEGACY_DISABLED")" == "legacy forwarding config" ]]; then
    ok "legacy config was preserved"
else
    not_ok "legacy config was preserved"
fi

# Simulate a container configured by v1.2.x, before profile revisions existed.
grep -v '^# homelab-logging-profile-revision:' "$MANAGED" > "$MANAGED.legacy"
mv "$MANAGED.legacy" "$MANAGED"
export MOCK_PCT_LIST=$'105 running - postgres\n106 stopped - offline\n'
export MOCK_STOPPED_CTIDS="106"
if "$ROOT/install.sh" --inventory postgres > "$TEST_TMP/legacy-inventory.out"; then ok "legacy installation inventory"; else not_ok "legacy installation inventory"; fi
assert_contains "$TEST_TMP/legacy-inventory.out" "1 (legacy)" "missing revision marker is inferred as legacy revision 1"
assert_contains "$TEST_TMP/legacy-inventory.out" "legacy metadata; sync will stamp revision" "legacy installation is recognized without reinstallation"
if "$ROOT/install.sh" --sync postgres --dry-run > "$TEST_TMP/sync-dry-run.out"; then ok "profile sync dry run"; else not_ok "profile sync dry run"; fi
assert_contains "$TEST_TMP/sync-dry-run.out" "previewed=1" "sync dry run previews the legacy upgrade"
assert_not_contains "$MANAGED" "# homelab-logging-profile-revision:" "sync dry run does not stamp metadata"
if "$ROOT/install.sh" --sync postgres > "$TEST_TMP/sync.out"; then ok "legacy profile synchronization"; else not_ok "legacy profile synchronization"; fi
assert_contains "$MANAGED" "# homelab-logging-profile-revision: 1" "first sync stamps the inferred revision"
assert_contains "$TEST_TMP/sync.out" "updated=1" "sync summary reports the updated LXC"

before="$(shasum -a 256 "$MANAGED" | awk '{print $1}')"
if "$ROOT/install.sh" 105 postgres > "$TEST_TMP/idempotent.out"; then ok "idempotent redeployment"; else not_ok "idempotent redeployment"; fi
assert_contains "$TEST_TMP/idempotent.out" "already current" "idempotent deployment avoids a restart"

if "$ROOT/install.sh" 105 postgres --status > "$TEST_TMP/status.out"; then ok "healthy status audit"; else not_ok "healthy status audit"; fi
assert_contains "$TEST_TMP/status.out" "Proxmox node label: patate-du-cluster" "status reports the installed node"
assert_contains "$TEST_TMP/status.out" "Result: 0 problem(s)" "status reports a healthy installation"

export HLL_NODE_OVERRIDE="caisse-ping"
if "$ROOT/install.sh" 105 --migrate > "$TEST_TMP/migrate.out"; then ok "post-migration refresh"; else not_ok "post-migration refresh"; fi
assert_contains "$TEST_TMP/migrate.out" "node patate-du-cluster -> caisse-ping" "migration reports the node change"
assert_contains "$MANAGED" "# homelab-logging-node: caisse-ping" "migration updates managed node metadata"
assert_contains "$MANAGED" 'node=\"caisse-ping\"' "migration updates transmitted node label"
if "$ROOT/install.sh" 105 --status > "$TEST_TMP/migrated-status.out"; then ok "status is healthy on the new node"; else not_ok "status is healthy on the new node"; fi
before="$(shasum -a 256 "$MANAGED" | awk '{print $1}')"

printf 'legacy config to restore\n' > "$LEGACY"
export MOCK_VALIDATE_FAIL=1
if "$ROOT/install.sh" 100 pbs > "$TEST_TMP/rollback.out" 2>&1; then
    not_ok "invalid replacement is rejected"
else
    ok "invalid replacement is rejected"
fi
unset MOCK_VALIDATE_FAIL
after="$(shasum -a 256 "$MANAGED" | awk '{print $1}')"
if [[ "$before" == "$after" ]]; then ok "failed deployment rolls back"; else not_ok "failed deployment rolls back"; fi
if [[ -f "$LEGACY" && "$(< "$LEGACY")" == "legacy config to restore" ]]; then
    ok "rollback reactivates legacy config"
else
    not_ok "rollback reactivates legacy config"
fi

PROFILE_V2="$TEST_TMP/services-v2"
cp -R "$ROOT/services" "$PROFILE_V2"
PROFILE_DRIFT="$TEST_TMP/services-drift"
cp -R "$ROOT/services" "$PROFILE_DRIFT"
jq '.notes += ["Changed without a revision bump."]' "$PROFILE_DRIFT/postgres.json" > "$PROFILE_DRIFT/postgres.json.tmp"
mv "$PROFILE_DRIFT/postgres.json.tmp" "$PROFILE_DRIFT/postgres.json"
if "$ROOT/install.sh" --profiles-dir "$PROFILE_DRIFT" --inventory postgres > "$TEST_TMP/drift-inventory.out"; then ok "same-revision hash inventory"; else not_ok "same-revision hash inventory"; fi
assert_contains "$TEST_TMP/drift-inventory.out" "hash drift at same revision" "inventory catches a forgotten revision bump"

jq '.profile_revision = 2' "$PROFILE_V2/postgres.json" > "$PROFILE_V2/postgres.json.tmp"
mv "$PROFILE_V2/postgres.json.tmp" "$PROFILE_V2/postgres.json"
if "$ROOT/install.sh" --profiles-dir "$PROFILE_V2" --inventory postgres > "$TEST_TMP/v2-inventory.out"; then ok "new profile revision inventory"; else not_ok "new profile revision inventory"; fi
assert_contains "$TEST_TMP/v2-inventory.out" "update available" "inventory reports a newer profile revision"
if "$ROOT/install.sh" --profiles-dir "$PROFILE_V2" --sync postgres > "$TEST_TMP/v2-sync.out"; then ok "new profile revision synchronization"; else not_ok "new profile revision synchronization"; fi
assert_contains "$MANAGED" "# homelab-logging-profile-revision: 2" "sync deploys the newer profile revision"

printf '1..%s\n' "$((pass + fail))"
printf '# %s passed, %s failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
