#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"
# shellcheck source=lib/pct.sh
source "$SCRIPT_DIR/lib/pct.sh"
# shellcheck source=lib/profile.sh
source "$SCRIPT_DIR/lib/profile.sh"
# shellcheck source=lib/rsyslog.sh
source "$SCRIPT_DIR/lib/rsyslog.sh"

SITE_CONFIG="$SCRIPT_DIR/config.json"
PROFILE_DIR="$SCRIPT_DIR/services"
ACTION="install"
DRY_RUN=0
CTID=""
PROFILE_NAME=""
NODE_NAME=""
MIGRATION_OLD_NODE=""
LOCK_DIR=""
ROLLBACK_NEEDED=0
ROLLBACK_CTID=""
ROLLBACK_REMOTE=""
ROLLBACK_BACKUP=""
ROLLBACK_HAD_PREVIOUS=0
ROLLBACK_LEGACY_RECORDS=""

usage() {
    cat <<'EOF'
homelab-logging v1 - deploy declarative rsyslog forwarding into Proxmox LXCs

Usage:
  ./install.sh <CTID> [PROFILE] [--dry-run]
  ./install.sh <CTID> [PROFILE] --status
  ./install.sh <CTID> --migrate [--dry-run]
  ./install.sh --inventory [PROFILE]
  ./install.sh --sync [PROFILE] [--dry-run]
  ./install.sh --list
  ./install.sh --validate [PROFILE]

Options:
  --config PATH         Site configuration (default: ./config.json)
  --profiles-dir PATH   Profile directory (default: ./services)
  --dry-run             Generate and display changes without modifying the LXC
  --status              Audit installation, service, destination, and sources
  --migrate             Refresh an installed profile after moving an LXC to this node
  --inventory           Show installed and available profile revisions on this node
  --sync                Reconcile previously managed LXCs on this node
  --list                List available profiles
  --validate            Validate the site config and one or all profiles
  --version             Print the version
  -h, --help            Show this help

Examples:
  ./install.sh 105 postgres
  ./install.sh 105                  # auto-detect PostgreSQL
  ./install.sh 116 docker --dry-run
  ./install.sh 100 pbs --status
  ./install.sh 105 --migrate       # run on the LXC's new Proxmox node
  ./install.sh --inventory
  ./install.sh --sync beszel --dry-run
  ./install.sh --sync beszel
EOF
}

rollback_deployment() {
    [[ "$ROLLBACK_NEEDED" -eq 1 ]] || return 0
    set +e
    warn "Rolling back rsyslog configuration in CT $ROLLBACK_CTID"
    if [[ "$ROLLBACK_HAD_PREVIOUS" -eq 1 ]]; then
        pct_exec "$ROLLBACK_CTID" cp "$ROLLBACK_BACKUP" "$ROLLBACK_REMOTE"
    else
        pct_exec "$ROLLBACK_CTID" rm -f "$ROLLBACK_REMOTE"
    fi
    if [[ -n "$ROLLBACK_LEGACY_RECORDS" ]]; then
        local legacy disabled
        while IFS='|' read -r legacy disabled; do
            [[ -n "$legacy" && -n "$disabled" ]] || continue
            if remote_file_exists "$ROLLBACK_CTID" "$disabled"; then
                pct_exec "$ROLLBACK_CTID" mv "$disabled" "$legacy"
            fi
        done <<< "$ROLLBACK_LEGACY_RECORDS"
    fi
    pct_exec "$ROLLBACK_CTID" systemctl restart rsyslog >/dev/null 2>&1 || true
    ROLLBACK_NEEDED=0
    set -e
}

on_exit() {
    local code=$?
    rollback_deployment
    release_lock
    cleanup
    trap - EXIT
    exit "$code"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

list_profiles() {
    local file
    printf '%-20s %-9s %-8s %s\n' PROFILE REVISION MODE DESCRIPTION
    print_hr
    while IFS= read -r file; do
        validate_profile "$file" || {
            printf '%-20s %-9s %-8s %s\n' "$(basename "$file" .json)" unknown INVALID "Profile failed validation"
            continue
        }
        local mode="journal"
        [[ "$(jq -r '.docker.enabled' "$file")" == "true" ]] && mode="docker"
        [[ "$(jq '.files | length' "$file")" -gt 0 ]] && mode="files"
        [[ "$(jq '.tasks | length' "$file")" -gt 0 ]] && mode="tasks"
        printf '%-20s %-9s %-8s %s\n' \
            "$(jq -r '.name' "$file")" "$(jq -r '.profile_revision' "$file")" "$mode" "$(jq -r '.description' "$file")"
    done < <(find "$PROFILE_DIR" -maxdepth 1 -type f -name '*.json' | sort)
}

active_legacy_configs() {
    local ctid="$1" remote="$2" legacy
    while IFS= read -r legacy; do
        [[ "$legacy" == "$remote" ]] && continue
        if remote_file_exists "$ctid" "$legacy"; then
            printf '%s\n' "$legacy"
        fi
    done < <(jq -r '.legacy_configs[]' "$SITE_CONFIG")
}

installed_marker() {
    local ctid="$1" remote="$2" marker="$3" temp
    remote_file_exists "$ctid" "$remote" || return 1
    temp="$(make_temp_file)"
    pull_remote_file "$ctid" "$remote" "$temp" >/dev/null
    sed -n "s/^# $marker: //p" "$temp" | head -1
    rm -f "$temp"
}

installed_profile() {
    installed_marker "$1" "$2" "homelab-logging-profile"
}

installed_node() {
    installed_marker "$1" "$2" "homelab-logging-node"
}

run_node_reconciliation() {
    local mode="$1" filter="${PROFILE_NAME:-}" remote ctid status metadata
    local installed_name installed_revision installed_hash installed_node_name
    local profile available_revision available_hash revision_label result output
    local managed=0 current=0 updated=0 previewed=0 failed=0 deferred=0 legacy=0
    local -a child_args
    remote="$(jq -r '.remote_config' "$SITE_CONFIG")"

    if [[ -n "$filter" ]]; then
        profile_path "$PROFILE_DIR" "$filter" >/dev/null
    fi
    if [[ "$mode" == "sync" && "$DRY_RUN" -eq 0 ]]; then
        require_root_for_write
    fi

    if [[ "$mode" == "inventory" ]]; then
        printf '%-7s %-20s %-12s %-12s %s\n' CTID PROFILE INSTALLED AVAILABLE RESULT
        print_hr
    fi

    while IFS=$'\t' read -r ctid status; do
        [[ -n "$ctid" ]] || continue
        if [[ "$status" != "running" ]]; then
            deferred=$((deferred + 1))
            if [[ "$mode" == "inventory" ]]; then
                printf '%-7s %-20s %-12s %-12s %s\n' "$ctid" unknown unknown unknown "stopped; not inspected"
            else
                warn "CT $ctid is stopped and could not be inspected"
            fi
            continue
        fi

        remote_file_exists "$ctid" "$remote" || continue
        metadata="$(make_temp_file)"
        register_cleanup "$metadata"
        if ! pull_remote_file "$ctid" "$remote" "$metadata" >/dev/null 2>&1; then
            warn "Could not read managed configuration from CT $ctid"
            failed=$((failed + 1))
            continue
        fi

        installed_name="$(sed -n 's/^# homelab-logging-profile: //p' "$metadata" | head -1)"
        [[ -n "$installed_name" ]] || continue
        [[ -z "$filter" || "$installed_name" == "$filter" ]] || continue
        managed=$((managed + 1))

        installed_revision="$(sed -n 's/^# homelab-logging-profile-revision: //p' "$metadata" | head -1)"
        installed_hash="$(sed -n 's/^# homelab-logging-profile-sha256: //p' "$metadata" | head -1)"
        installed_node_name="$(sed -n 's/^# homelab-logging-node: //p' "$metadata" | head -1)"
        revision_label="$installed_revision"
        if [[ -z "$installed_revision" ]]; then
            installed_revision=1
            revision_label="1 (legacy)"
            legacy=$((legacy + 1))
        elif [[ ! "$installed_revision" =~ ^[1-9][0-9]*$ ]]; then
            revision_label="invalid"
        fi

        profile="$PROFILE_DIR/$installed_name.json"
        if [[ ! -f "$profile" ]] || ! validate_profile "$profile"; then
            result="local profile missing or invalid"
            failed=$((failed + 1))
            if [[ "$mode" == "inventory" ]]; then
                printf '%-7s %-20s %-12s %-12s %s\n' "$ctid" "$installed_name" "$revision_label" unknown "$result"
            else
                warn "CT $ctid references unavailable profile '$installed_name'"
            fi
            continue
        fi

        available_revision="$(jq -r '.profile_revision' "$profile")"
        available_hash="$(sha256_file "$profile")"
        if [[ "$revision_label" == "invalid" ]]; then
            result="invalid installed revision"
        elif [[ "$revision_label" == *"(legacy)"* ]]; then
            result="legacy metadata; sync will stamp revision"
        elif [[ "$installed_revision" -lt "$available_revision" ]]; then
            result="update available"
        elif [[ "$installed_revision" -gt "$available_revision" ]]; then
            result="local profile is older"
        elif [[ "$installed_hash" != "$available_hash" ]]; then
            result="hash drift at same revision"
        elif [[ "$installed_node_name" != "$NODE_NAME" ]]; then
            result="node label needs refresh"
        else
            result="current"
            current=$((current + 1))
        fi

        if [[ "$mode" == "inventory" ]]; then
            printf '%-7s %-20s %-12s %-12s %s\n' \
                "$ctid" "$installed_name" "$revision_label" "$available_revision" "$result"
            continue
        fi

        info "Reconciling CT $ctid with profile $installed_name (installed: $revision_label, available: $available_revision)"
        output="$(make_temp_file)"
        register_cleanup "$output"
        child_args=(
            bash "$SCRIPT_DIR/install.sh" "$ctid" "$installed_name"
            --config "$SITE_CONFIG"
            --profiles-dir "$PROFILE_DIR"
        )
        [[ "$DRY_RUN" -eq 1 ]] && child_args+=(--dry-run)
        if "${child_args[@]}" >"$output" 2>&1; then
            cat "$output"
            if [[ "$DRY_RUN" -eq 1 ]]; then
                previewed=$((previewed + 1))
            elif grep -Fq "Configuration is already current" "$output"; then
                current=$((current + 1))
            else
                updated=$((updated + 1))
            fi
        else
            cat "$output" >&2
            failed=$((failed + 1))
        fi
        print_hr
    done < <(list_local_containers)

    if [[ "$mode" == "inventory" ]]; then
        printf '\nManaged: %s  Current: %s  Legacy: %s  Deferred stopped: %s  Problems: %s\n' \
            "$managed" "$current" "$legacy" "$deferred" "$failed"
    else
        printf '\nSync summary: matched=%s updated=%s current=%s previewed=%s failed=%s stopped-uninspected=%s\n' \
            "$managed" "$updated" "$current" "$previewed" "$failed" "$deferred"
    fi
    [[ "$failed" -eq 0 ]]
}

prepare_docker_sources() {
    local ctid="$1" profile="$2" output="$3"
    : > "$output"
    if [[ "$(jq -r '.docker.enabled' "$profile")" == "true" ]]; then
        discover_docker_sources "$ctid" > "$output"
    fi
}

check_required_sources() {
    local ctid="$1" profile="$2" path missing=0
    while IFS= read -r path; do
        [[ -n "$path" ]] || continue
        if ! source_path_exists "$ctid" "$path"; then
            printf 'Required log source has no matches in CT %s: %s\n' "$ctid" "$path" >&2
            missing=1
        fi
    done < <(jq -r '(.files + .tasks)[] | select(.required == true) | .path' "$profile")
    [[ "$missing" -eq 0 ]]
}

print_profile_notes() {
    local profile="$1"
    if [[ "$(jq '.notes | length' "$profile")" -gt 0 ]]; then
        printf '\nProfile notes:\n'
        jq -r '.notes[] | "  - " + .' "$profile"
    fi
}

run_status() {
    local ctid="$1" profile="$2" generated="$3" docker_sources="$4"
    local remote target port expected_name expected_revision actual_name actual_revision actual_node remote_copy problems=0 path required label
    remote="$(jq -r '.remote_config' "$SITE_CONFIG")"
    target="$(jq -r '.alloy.host' "$SITE_CONFIG")"
    port="$(jq -r '.alloy.port' "$SITE_CONFIG")"
    expected_name="$(jq -r '.name' "$profile")"
    expected_revision="$(jq -r '.profile_revision' "$profile")"

    printf 'Audit for CT %s (%s)\n' "$ctid" "$(pct_exec "$ctid" hostname 2>/dev/null || printf unknown)"
    print_hr

    if pct_exec "$ctid" sh -c 'command -v rsyslogd >/dev/null 2>&1'; then
        printf '[ok]   rsyslog is installed\n'
    else
        printf '[fail] rsyslog is not installed\n'
        problems=$((problems + 1))
    fi

    if pct_exec "$ctid" systemctl is-active --quiet rsyslog; then
        printf '[ok]   rsyslog is running\n'
    else
        printf '[fail] rsyslog is not running\n'
        problems=$((problems + 1))
    fi

    if remote_file_exists "$ctid" "$remote"; then
        remote_copy="$(make_temp_file)"
        register_cleanup "$remote_copy"
        pull_remote_file "$ctid" "$remote" "$remote_copy" >/dev/null
        actual_name="$(sed -n 's/^# homelab-logging-profile: //p' "$remote_copy" | head -1)"
        actual_revision="$(sed -n 's/^# homelab-logging-profile-revision: //p' "$remote_copy" | head -1)"
        actual_node="$(sed -n 's/^# homelab-logging-node: //p' "$remote_copy" | head -1)"
        if [[ "$actual_name" == "$expected_name" ]]; then
            printf '[ok]   installed profile: %s\n' "$actual_name"
        else
            printf '[fail] installed profile is %s; expected %s\n' "${actual_name:-unknown}" "$expected_name"
            problems=$((problems + 1))
        fi
        if [[ "$actual_revision" == "$expected_revision" ]]; then
            printf '[ok]   installed profile revision: %s\n' "$actual_revision"
        elif [[ -z "$actual_revision" && "$expected_revision" == "1" ]]; then
            printf '[warn] installed profile revision: 1 (legacy marker inferred; run --sync %s)\n' "$expected_name"
        else
            printf '[fail] installed profile revision is %s; available revision is %s\n' "${actual_revision:-missing}" "$expected_revision"
            problems=$((problems + 1))
        fi
        if [[ "$actual_node" == "$NODE_NAME" ]]; then
            printf '[ok]   Proxmox node label: %s\n' "$actual_node"
        else
            printf '[fail] Proxmox node label is %s; current node is %s (run --migrate)\n' "${actual_node:-missing}" "$NODE_NAME"
            problems=$((problems + 1))
        fi
        if cmp -s "$remote_copy" "$generated"; then
            printf '[ok]   deployed configuration matches generated configuration\n'
        else
            printf '[fail] deployed configuration has drifted\n'
            problems=$((problems + 1))
        fi
    else
        printf '[fail] managed configuration is not installed: %s\n' "$remote"
        problems=$((problems + 1))
    fi

    local legacy
    while IFS= read -r legacy; do
        [[ -n "$legacy" ]] || continue
        if remote_file_exists "$ctid" "$legacy"; then
            printf '[fail] legacy configuration is still active: %s\n' "$legacy"
            problems=$((problems + 1))
        elif source_path_exists "$ctid" "$legacy.disabled.*"; then
            printf '[ok]   legacy configuration is disabled and preserved: %s\n' "$legacy"
        else
            printf '[ok]   legacy configuration is not present: %s\n' "$legacy"
        fi
    done < <(jq -r '.legacy_configs[]' "$SITE_CONFIG")

    if alloy_reachable "$ctid" "$target" "$port"; then
        printf '[ok]   Alloy is reachable at %s:%s/tcp\n' "$target" "$port"
    else
        printf '[fail] Alloy is not reachable at %s:%s/tcp\n' "$target" "$port"
        problems=$((problems + 1))
    fi

    while IFS=$'\t' read -r path required label; do
        [[ -n "$path" ]] || continue
        if source_path_exists "$ctid" "$path"; then
            printf '[ok]   source exists: %s (%s)\n' "$path" "$label"
        elif [[ "$required" == "true" ]]; then
            printf '[fail] required source missing: %s (%s)\n' "$path" "$label"
            problems=$((problems + 1))
        else
            printf '[warn] optional source absent: %s (%s)\n' "$path" "$label"
        fi
    done < <(jq -r '(.files + .tasks)[] | [.path,(.required // false),.service] | @tsv' "$profile")

    if [[ "$(jq -r '.docker.enabled' "$profile")" == "true" ]]; then
        if [[ -s "$docker_sources" ]]; then
            printf '[ok]   Docker log mappings: %s\n' "$(wc -l < "$docker_sources" | tr -d ' ')"
        else
            printf '[warn] no Docker json-file log paths were discovered\n'
        fi
    fi

    print_profile_notes "$profile"
    printf '\nResult: %s problem(s)\n' "$problems"
    [[ "$problems" -eq 0 ]]
}

deploy_config() {
    local ctid="$1" profile="$2" generated="$3"
    local remote current backup timestamp test_service profile_name active_legacy legacy disabled
    remote="$(jq -r '.remote_config' "$SITE_CONFIG")"
    profile_name="$(jq -r '.name' "$profile")"
    test_service="$(jq -r '.test_service' "$profile")"

    ensure_rsyslog "$ctid"
    check_required_paths "$ctid" "$profile" || die "Required path check failed"
    check_required_sources "$ctid" "$profile" || die "Required source check failed"

    current="$(make_temp_file)"
    register_cleanup "$current"
    active_legacy="$(active_legacy_configs "$ctid" "$remote")"
    if remote_file_exists "$ctid" "$remote"; then
        pull_remote_file "$ctid" "$remote" "$current" >/dev/null
        if cmp -s "$current" "$generated" && [[ -z "$active_legacy" ]]; then
            info "Configuration is already current for profile $profile_name"
            pct_exec "$ctid" systemctl enable --now rsyslog >/dev/null
            return 0
        fi
    fi

    timestamp="$(date +%Y%m%d-%H%M%S)-$$"
    ROLLBACK_CTID="$ctid"
    ROLLBACK_REMOTE="$remote"
    ROLLBACK_HAD_PREVIOUS=0
    ROLLBACK_BACKUP=""
    ROLLBACK_LEGACY_RECORDS=""
    if remote_file_exists "$ctid" "$remote"; then
        backup="$remote.bak.$timestamp"
        pct_exec "$ctid" cp "$remote" "$backup"
        ROLLBACK_HAD_PREVIOUS=1
        ROLLBACK_BACKUP="$backup"
        info "Backup created: $backup"
    fi

    ROLLBACK_NEEDED=1
    if [[ -n "$active_legacy" ]]; then
        while IFS= read -r legacy; do
            [[ -n "$legacy" ]] || continue
            disabled="$legacy.disabled.$timestamp"
            ROLLBACK_LEGACY_RECORDS+="$legacy|$disabled"$'\n'
            pct_exec "$ctid" mv "$legacy" "$disabled"
            info "Legacy configuration disabled and preserved: $disabled"
        done <<< "$active_legacy"
    fi

    push_remote_file "$ctid" "$generated" "$remote"
    if ! pct_exec "$ctid" rsyslogd -N1; then
        die "rsyslog validation failed; the previous configuration will be restored"
    fi
    if ! pct_exec "$ctid" systemctl enable rsyslog >/dev/null; then
        die "Could not enable rsyslog; the previous configuration will be restored"
    fi
    if ! pct_exec "$ctid" systemctl restart rsyslog; then
        die "rsyslog restart failed; the previous configuration will be restored"
    fi
    if ! pct_exec "$ctid" systemctl is-active --quiet rsyslog; then
        die "rsyslog is not active after restart; the previous configuration will be restored"
    fi
    ROLLBACK_NEEDED=0

    pct_exec "$ctid" logger -t "$test_service" "homelab-logging v$HLL_VERSION forwarding test for profile $profile_name" || true
    info "Installed profile $profile_name in CT $ctid"
    printf 'Suggested LogQL: {cluster="%s", host="%s"}\n' \
        "$(jq -r '.cluster' "$SITE_CONFIG")" "$(pct_exec "$ctid" hostname)"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --config)
            [[ $# -ge 2 ]] || die "--config requires a path"
            SITE_CONFIG="$2"
            shift 2
            ;;
        --profiles-dir)
            [[ $# -ge 2 ]] || die "--profiles-dir requires a path"
            PROFILE_DIR="$2"
            shift 2
            ;;
        --dry-run) DRY_RUN=1; shift ;;
        --status) ACTION="status"; shift ;;
        --migrate) ACTION="migrate"; shift ;;
        --inventory) ACTION="inventory"; shift ;;
        --sync) ACTION="sync"; shift ;;
        --list) ACTION="list"; shift ;;
        --validate) ACTION="validate"; shift ;;
        --version) printf '%s\n' "$HLL_VERSION"; exit 0 ;;
        -h|--help) usage; exit 0 ;;
        --*) die "Unknown option: $1" ;;
        *)
            if [[ -z "$CTID" && "$1" =~ ^[0-9]+$ ]]; then
                CTID="$1"
            elif [[ -z "$PROFILE_NAME" ]]; then
                PROFILE_NAME="$1"
            else
                die "Unexpected argument: $1"
            fi
            shift
            ;;
    esac
done

require_command jq
[[ -f "$SITE_CONFIG" ]] || die "Site configuration not found: $SITE_CONFIG"
[[ -d "$PROFILE_DIR" ]] || die "Profile directory not found: $PROFILE_DIR"
validate_site_config "$SITE_CONFIG"

case "$ACTION" in
    list)
        list_profiles
        exit 0
        ;;
    validate)
        if [[ -n "$PROFILE_NAME" ]]; then
            profile_path "$PROFILE_DIR" "$PROFILE_NAME" >/dev/null
            info "Site configuration and profile '$PROFILE_NAME' are valid"
        else
            validate_all_profiles "$PROFILE_DIR" || die "Profile validation failed"
            info "Site configuration and all profiles are valid"
        fi
        exit 0
        ;;
    inventory|sync)
        [[ -z "$CTID" ]] || die "--$ACTION does not accept a CTID"
        [[ "$ACTION" == "sync" || "$DRY_RUN" -eq 0 ]] || die "--dry-run is only valid with --sync"
        require_pct
        NODE_NAME="$(proxmox_node_name)"
        run_node_reconciliation "$ACTION"
        exit $?
        ;;
esac

[[ -n "$CTID" ]] || die "A CTID is required"
require_pct
verify_container "$CTID"
NODE_NAME="$(proxmox_node_name)"

REMOTE_CONFIG="$(jq -r '.remote_config' "$SITE_CONFIG")"
if [[ "$ACTION" == "migrate" ]]; then
    [[ -z "$PROFILE_NAME" ]] || die "--migrate uses the installed profile; do not specify a profile"
    PROFILE_NAME="$(installed_profile "$CTID" "$REMOTE_CONFIG" || true)"
    [[ -n "$PROFILE_NAME" ]] || die "CT $CTID has no managed profile to migrate; run a normal installation first"
    MIGRATION_OLD_NODE="$(installed_node "$CTID" "$REMOTE_CONFIG" || true)"
    info "Refreshing CT $CTID after migration: node ${MIGRATION_OLD_NODE:-unknown} -> $NODE_NAME (profile: $PROFILE_NAME)"
fi

if [[ -z "$PROFILE_NAME" && "$ACTION" == "status" ]]; then
    PROFILE_NAME="$(installed_profile "$CTID" "$REMOTE_CONFIG" || true)"
    if [[ -n "$PROFILE_NAME" && ! -f "$PROFILE_DIR/$PROFILE_NAME.json" ]]; then
        warn "Installed configuration names unknown profile '$PROFILE_NAME'; using auto-detection for comparison"
        PROFILE_NAME=""
    fi
fi

if [[ -n "$PROFILE_NAME" ]]; then
    PROFILE_FILE="$(profile_path "$PROFILE_DIR" "$PROFILE_NAME")"
else
    info "Detecting profile for CT $CTID"
    PROFILE_FILE="$(detect_profile "$CTID" "$PROFILE_DIR")"
    PROFILE_NAME="$(jq -r '.name' "$PROFILE_FILE")"
    info "Detected profile: $PROFILE_NAME"
fi

DOCKER_SOURCES="$(make_temp_file)"
GENERATED_CONFIG="$(make_temp_file)"
register_cleanup "$DOCKER_SOURCES"
register_cleanup "$GENERATED_CONFIG"
prepare_docker_sources "$CTID" "$PROFILE_FILE" "$DOCKER_SOURCES"
generate_rsyslog_config "$SITE_CONFIG" "$PROFILE_FILE" "$DOCKER_SOURCES" "$NODE_NAME" > "$GENERATED_CONFIG"

if [[ "$ACTION" == "status" ]]; then
    run_status "$CTID" "$PROFILE_FILE" "$GENERATED_CONFIG" "$DOCKER_SOURCES"
    exit $?
fi

check_required_paths "$CTID" "$PROFILE_FILE" || die "Required path check failed"
check_required_sources "$CTID" "$PROFILE_FILE" || die "Required source check failed"

if [[ "$DRY_RUN" -eq 1 ]]; then
    printf 'Dry run for CT %s using profile %s\n' "$CTID" "$PROFILE_NAME"
    printf 'Would install rsyslog if absent, back up %s, validate, restart, and send a test message.\n' "$REMOTE_CONFIG"
    ACTIVE_LEGACY="$(active_legacy_configs "$CTID" "$REMOTE_CONFIG")"
    if [[ -n "$ACTIVE_LEGACY" ]]; then
        while IFS= read -r legacy; do
            [[ -n "$legacy" ]] || continue
            printf 'Would back up and deactivate legacy configuration: %s\n' "$legacy"
        done <<< "$ACTIVE_LEGACY"
    fi
    printf '\n'
    print_hr
    cat "$GENERATED_CONFIG"
    print_hr
    print_profile_notes "$PROFILE_FILE"
    exit 0
fi

require_root_for_write
acquire_lock "$CTID"
deploy_config "$CTID" "$PROFILE_FILE" "$GENERATED_CONFIG"
if [[ "$ACTION" == "migrate" ]]; then
    info "Migration refresh complete: CT $CTID is labeled with node=$NODE_NAME"
fi
print_profile_notes "$PROFILE_FILE"
