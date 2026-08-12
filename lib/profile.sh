#!/usr/bin/env bash

validate_profile() {
    local file="$1"
    jq -e '
        def safe_name: type == "string" and test("^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$");
        def safe_path: type == "string" and startswith("/") and (contains("\n") | not) and (contains("\"") | not);
        def probe:
            type == "object" and
            ((keys - ["type","value"]) | length == 0) and
            (.type == "path" or .type == "service" or .type == "command" or .type == "package") and
            (.value | type == "string" and length > 0 and length <= 255 and (contains("\n") | not));
        def source:
            type == "object" and
            ((keys - ["path","service","required","facility","severity","include_filename"]) | length == 0) and
            (.path | safe_path) and
            (.service | safe_name) and
            ((.required // false) | type == "boolean") and
            ((.facility // "local5") | type == "string" and test("^(auth|authpriv|cron|daemon|kern|local[0-7]|mail|news|syslog|user|uucp)$")) and
            ((.severity // "info") | type == "string" and test("^(debug|info|notice|warning|err|crit|alert|emerg)$")) and
            ((.include_filename // false) | type == "boolean");
        type == "object" and
        ((keys - ["schema_version","profile_revision","name","description","journal","required_paths","files","tasks","docker","detect","test_service","notes"]) | length == 0) and
        .schema_version == 1 and
        (.profile_revision | type == "number" and floor == . and . >= 1 and . <= 2147483647) and
        (.name | safe_name) and
        (.description | type == "string" and length > 0) and
        (.journal | type == "boolean") and
        (.required_paths | type == "array" and all(.[]; safe_path)) and
        (.files | type == "array" and all(.[]; source)) and
        (.tasks | type == "array" and all(.[]; source)) and
        (.docker | type == "object") and
        ((.docker | keys) - ["enabled"] | length == 0) and
        (.docker.enabled | type == "boolean") and
        (.detect | type == "object") and
        ((.detect | keys) - ["mode","priority","probes"] | length == 0) and
        (.detect.mode == "all" or .detect.mode == "any") and
        (.detect.priority | type == "number" and floor == . and . >= 0 and . <= 1000) and
        (.detect.probes | type == "array" and all(.[]; probe)) and
        (.test_service | safe_name) and
        (.notes | type == "array" and all(.[]; type == "string")) and
        (.journal or (.files | length > 0) or (.tasks | length > 0) or .docker.enabled)
    ' "$file" >/dev/null 2>&1 || return 1

    local expected
    expected="$(basename "$file" .json)"
    [[ "$(jq -r '.name' "$file")" == "$expected" ]]
}

validate_all_profiles() {
    local directory="$1"
    local found=0 file
    while IFS= read -r file; do
        found=1
        if ! validate_profile "$file"; then
            printf 'Invalid profile: %s\n' "$file" >&2
            return 1
        fi
    done < <(find "$directory" -maxdepth 1 -type f -name '*.json' | sort)
    [[ "$found" -eq 1 ]] || {
        printf 'No profiles found in %s\n' "$directory" >&2
        return 1
    }
}

profile_path() {
    local directory="$1" name="$2"
    [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || die "Invalid profile name: $name"
    local path="$directory/$name.json"
    [[ -f "$path" ]] || die "Unknown profile '$name'. Run --list to see available profiles."
    validate_profile "$path" || die "Profile failed validation: $path"
    printf '%s\n' "$path"
}

probe_matches() {
    local ctid="$1" type="$2" value="$3"
    case "$type" in
        path) pct_exec "$ctid" test -e "$value" >/dev/null 2>&1 ;;
        service) pct_exec "$ctid" systemctl cat "$value" >/dev/null 2>&1 ;;
        command) pct_exec "$ctid" sh -c 'command -v "$1" >/dev/null 2>&1' _ "$value" >/dev/null 2>&1 ;;
        package) pct_exec "$ctid" dpkg-query -W "$value" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}

detect_profile() {
    local ctid="$1" directory="$2"
    local best="" best_score=-1 ties="" file mode priority total matched type value ok

    while IFS= read -r file; do
        validate_profile "$file" || continue
        total="$(jq '.detect.probes | length' "$file")"
        [[ "$total" -gt 0 ]] || continue
        mode="$(jq -r '.detect.mode' "$file")"
        priority="$(jq -r '.detect.priority' "$file")"
        matched=0

        while IFS=$'\t' read -r type value; do
            if probe_matches "$ctid" "$type" "$value"; then
                matched=$((matched + 1))
            fi
        done < <(jq -r '.detect.probes[] | [.type,.value] | @tsv' "$file")

        ok=0
        if [[ "$mode" == "all" && "$matched" -eq "$total" ]]; then
            ok=1
        elif [[ "$mode" == "any" && "$matched" -gt 0 ]]; then
            ok=1
        fi

        if [[ "$ok" -eq 1 ]]; then
            local score=$((priority * 100 + matched))
            if [[ "$score" -gt "$best_score" ]]; then
                best="$file"
                best_score="$score"
                ties=""
            elif [[ "$score" -eq "$best_score" ]]; then
                ties="$ties $(basename "$file" .json)"
            fi
        fi
    done < <(find "$directory" -maxdepth 1 -type f -name '*.json' | sort)

    [[ -n "$best" ]] || die "No profile matched CT $ctid; specify one explicitly"
    [[ -z "$ties" ]] || die "Profile detection is ambiguous: $(basename "$best" .json)$ties"
    printf '%s\n' "$best"
}

check_required_paths() {
    local ctid="$1" profile="$2" missing=0 path
    while IFS= read -r path; do
        if ! pct_exec "$ctid" test -e "$path" >/dev/null 2>&1; then
            printf 'Missing required path in CT %s: %s\n' "$ctid" "$path" >&2
            missing=1
        fi
    done < <(jq -r '.required_paths[]' "$profile")
    [[ "$missing" -eq 0 ]]
}

discover_docker_sources() {
    local ctid="$1"
    pct_exec "$ctid" sh -c '
        for id in $(docker ps -aq 2>/dev/null); do
            docker inspect --format "{{.Name}}|{{.LogPath}}" "$id"
        done
    ' 2>/dev/null | awk -F '|' '
        NF == 2 {
            sub(/^\//, "", $1)
            if ($1 ~ /^[A-Za-z0-9][A-Za-z0-9_.-]*$/ && $2 ~ /^\// && $2 !~ /["\\]/)
                print $1 "|" $2
        }
    ' | sort -u
}
