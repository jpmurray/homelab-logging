#!/usr/bin/env bash

emit_forward_template() {
    local template_name="$1" job="$2" cluster="$3" location="$4" role="$5" node="$6"
    cat <<EOF
template(name="$template_name" type="list") {
    constant(value="<")
    property(name="pri")
    constant(value=">1 ")
    property(name="timereported" dateFormat="rfc3339")
    constant(value=" ")
    property(name="hostname")
    constant(value=" ")
    property(name="app-name" position.from="1" position.to="48")
    constant(value=" - - [homelab@32473 cluster=\"$cluster\" location=\"$location\" role=\"$role\" node=\"$node\" job=\"$job\"] ")
    property(name="msg" droplastlf="on")
    constant(value="\\n")
}

EOF
}

emit_task_template() {
    local cluster="$1" location="$2" role="$3" node="$4"
    cat <<EOF
template(name="AlloyTaskForward" type="list") {
    constant(value="<")
    property(name="pri")
    constant(value=">1 ")
    property(name="timereported" dateFormat="rfc3339")
    constant(value=" ")
    property(name="hostname")
    constant(value=" ")
    property(name="app-name" position.from="1" position.to="48")
    constant(value=" - - [homelab@32473 cluster=\"$cluster\" location=\"$location\" role=\"$role\" node=\"$node\" job=\"syslog\"] [")
    property(name="\$.task_id")
    constant(value="] ")
    property(name="msg" droplastlf="on")
    constant(value="\\n")
}

EOF
}

emit_forward_action() {
    local target="$1" port="$2" template="$3" queue="$4"
    cat <<EOF
    action(
        type="omfwd"
        target="$target"
        port="$port"
        protocol="tcp"
        template="$template"
        queue.type="linkedList"
        queue.filename="$queue"
        queue.saveOnShutdown="on"
        action.resumeRetryCount="-1"
    )
EOF
}

emit_ruleset() {
    local name="$1" target="$2" port="$3" template="$4" queue="$5"
    printf 'ruleset(name="%s") {\n' "$name"
    emit_forward_action "$target" "$port" "$template" "$queue"
    printf '    stop\n}\n\n'
}

emit_task_ruleset() {
    local target="$1" port="$2"
    cat <<'EOF'
ruleset(name="alloy_tasks") {
    set $.task_id = re_extract($!metadata!filename, "([^/]+)\$", 0, 1, "unknown-task");
EOF
    emit_forward_action "$target" "$port" "AlloyTaskForward" "alloy-tasks"
    printf '    stop\n}\n\n'
}

emit_imfile_input() {
    local path="$1" service="$2" facility="$3" severity="$4" ruleset="$5" metadata="$6"
    cat <<EOF
input(
    type="imfile"
    File="$path"
    Tag="$service:"
    Facility="$facility"
    Severity="$severity"
    PersistStateInterval="100"
    freshStartTail="on"
    addMetadata="$metadata"
    Ruleset="$ruleset"
)

EOF
}

generate_rsyslog_config() {
    local site="$1" profile="$2" docker_sources_file="$3" node="$4"
    local cluster location role target port profile_name profile_revision profile_hash site_hash
    local files_count tasks_count docker_enabled journal_enabled
    cluster="$(jq -r '.cluster' "$site")"
    location="$(jq -r '.location' "$site")"
    role="$(jq -r '.origin_role' "$site")"
    target="$(jq -r '.alloy.host' "$site")"
    port="$(jq -r '.alloy.port' "$site")"
    profile_name="$(jq -r '.name' "$profile")"
    profile_revision="$(jq -r '.profile_revision' "$profile")"
    profile_hash="$(sha256_file "$profile")"
    site_hash="$(sha256_file "$site")"
    files_count="$(jq '.files | length' "$profile")"
    tasks_count="$(jq '.tasks | length' "$profile")"
    docker_enabled="$(jq -r '.docker.enabled' "$profile")"
    journal_enabled="$(jq -r '.journal' "$profile")"

    cat <<EOF
# Managed by homelab-logging v$HLL_VERSION. Local edits will be replaced.
# homelab-logging-profile: $profile_name
# homelab-logging-profile-revision: $profile_revision
# homelab-logging-node: $node
# homelab-logging-profile-sha256: $profile_hash
# homelab-logging-site-sha256: $site_hash

EOF

    if [[ "$files_count" -gt 0 || "$tasks_count" -gt 0 || "$docker_enabled" == "true" ]]; then
        printf 'module(load="imfile" PollingInterval="10")\n\n'
    fi

    emit_forward_template "AlloySyslogForward" "syslog" "$cluster" "$location" "$role" "$node"
    if [[ "$docker_enabled" == "true" ]]; then
        emit_forward_template "AlloyDockerForward" "docker" "$cluster" "$location" "$role" "$node"
    fi
    if jq -e '.tasks[]? | select(.include_filename == true)' "$profile" >/dev/null; then
        emit_task_template "$cluster" "$location" "$role" "$node"
    fi

    if [[ "$files_count" -gt 0 || "$tasks_count" -gt 0 ]]; then
        emit_ruleset "alloy_files" "$target" "$port" "AlloySyslogForward" "alloy-files"
    fi
    if jq -e '.tasks[]? | select(.include_filename == true)' "$profile" >/dev/null; then
        emit_task_ruleset "$target" "$port"
    fi
    if [[ "$docker_enabled" == "true" ]]; then
        emit_ruleset "alloy_docker" "$target" "$port" "AlloyDockerForward" "alloy-docker"
    fi

    local path service facility severity include_filename ruleset metadata
    while IFS=$'\t' read -r path service facility severity; do
        [[ -n "$path" ]] || continue
        emit_imfile_input "$path" "$service" "$facility" "$severity" "alloy_files" "off"
    done < <(jq -r '.files[] | [.path,.service,(.facility // "local5"),(.severity // "info")] | @tsv' "$profile")

    while IFS=$'\t' read -r path service facility severity include_filename; do
        [[ -n "$path" ]] || continue
        ruleset="alloy_files"
        metadata="off"
        if [[ "$include_filename" == "true" ]]; then
            ruleset="alloy_tasks"
            metadata="on"
        fi
        emit_imfile_input "$path" "$service" "$facility" "$severity" "$ruleset" "$metadata"
    done < <(jq -r '.tasks[] | [.path,.service,(.facility // "local5"),(.severity // "info"),(.include_filename // false)] | @tsv' "$profile")

    if [[ "$docker_enabled" == "true" && -s "$docker_sources_file" ]]; then
        while IFS='|' read -r service path; do
            [[ -n "$service" && -n "$path" ]] || continue
            emit_imfile_input "$path" "$service" "local6" "info" "alloy_docker" "off"
        done < "$docker_sources_file"
    fi

    if [[ "$journal_enabled" == "true" ]]; then
        printf '# Forward messages received through the container syslog/journal path.\n'
        emit_forward_action "$target" "$port" "AlloySyslogForward" "alloy-journal"
    fi
}
