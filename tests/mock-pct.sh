#!/usr/bin/env bash
set -euo pipefail

root="${MOCK_CONTAINER_ROOT:?MOCK_CONTAINER_ROOT is required}"
op="${1:-}"
shift || true

record() {
    if [[ -n "${MOCK_PCT_LOG:-}" ]]; then
        printf '%s\n' "$*" >> "$MOCK_PCT_LOG"
    fi
}

contains_word() {
    local list="$1" needle="$2" item
    for item in $list; do
        [[ "$item" == "$needle" ]] && return 0
    done
    return 1
}

translate() {
    printf '%s%s\n' "$root" "$1"
}

case "$op" in
    list)
        printf 'VMID Status Lock Name\n'
        printf '%b' "${MOCK_PCT_LIST:-105 running - mock-lxc\n}"
        ;;
    status)
        ctid="${1:-}"
        if contains_word "${MOCK_STOPPED_CTIDS:-}" "$ctid"; then
            printf 'status: stopped\n'
        else
            printf 'status: %s\n' "${MOCK_CT_STATUS:-running}"
        fi
        ;;
    pull)
        ctid="$1" remote="$2" local_path="$3"
        record "pull $ctid $remote"
        cp "$(translate "$remote")" "$local_path"
        ;;
    push)
        ctid="$1" local_path="$2" remote="$3"
        record "push $ctid $remote"
        mkdir -p "$(dirname "$(translate "$remote")")"
        cp "$local_path" "$(translate "$remote")"
        ;;
    exec)
        ctid="$1"
        shift
        [[ "${1:-}" == "--" ]] && shift
        record "exec $ctid $*"
        command_name="${1:-}"
        shift || true
        case "$command_name" in
            hostname)
                printf '%s\n' "${MOCK_HOSTNAME:-mock-lxc}"
                ;;
            test)
                test_op="$1" path="$2"
                test "$test_op" "$(translate "$path")"
                ;;
            cp)
                cp "$(translate "$1")" "$(translate "$2")"
                ;;
            mv)
                mv "$(translate "$1")" "$(translate "$2")"
                ;;
            rm)
                [[ "${1:-}" == "-f" ]] && shift
                rm -f "$(translate "$1")"
                ;;
            sh)
                [[ "${1:-}" == "-c" ]] || exit 1
                script="$2"
                shift 2
                if [[ "$script" == *'docker inspect'* ]]; then
                    printf '%b' "${MOCK_DOCKER_SOURCES:-}"
                elif [[ "$script" == *'command -v'* ]]; then
                    needle="${2:-rsyslogd}"
                    if [[ "$needle" == "rsyslogd" ]]; then
                        [[ "${MOCK_RSYSLOG_INSTALLED:-1}" == "1" ]]
                    else
                        contains_word "${MOCK_COMMANDS:-}" "$needle"
                    fi
                fi
                ;;
            bash)
                [[ "${1:-}" == "-c" ]] || exit 1
                script="$2"
                shift 2
                if [[ "$script" == *'compgen -G'* ]]; then
                    pattern="${2:-}"
                    compgen -G "$root$pattern" | grep -q .
                else
                    [[ "${MOCK_ALLOY_REACHABLE:-1}" == "1" ]]
                fi
                ;;
            systemctl)
                sub="$1"
                shift
                case "$sub" in
                    cat) contains_word "${MOCK_SERVICES:-}" "$1" ;;
                    is-active) [[ "${MOCK_RSYSLOG_ACTIVE:-1}" == "1" ]] ;;
                    restart) [[ "${MOCK_RESTART_FAIL:-0}" != "1" ]] ;;
                    enable) [[ "${MOCK_ENABLE_FAIL:-0}" != "1" ]] ;;
                    *) exit 0 ;;
                esac
                ;;
            dpkg-query)
                [[ "${1:-}" == "-W" ]] && contains_word "${MOCK_PACKAGES:-}" "$2"
                ;;
            rsyslogd)
                [[ "${MOCK_VALIDATE_FAIL:-0}" != "1" ]]
                ;;
            apt-get|env|logger)
                exit 0
                ;;
            *)
                printf 'mock-pct: unsupported exec command: %s\n' "$command_name" >&2
                exit 1
                ;;
        esac
        ;;
    *)
        printf 'mock-pct: unsupported operation: %s\n' "$op" >&2
        exit 1
        ;;
esac
