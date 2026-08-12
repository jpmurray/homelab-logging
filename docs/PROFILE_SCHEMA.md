# Profile schema v1

The canonical machine-readable schema is `schema/service-profile.schema.json`. Runtime validation is implemented directly in Go with the standard library, including rejection of unknown JSON fields.

Every profile contains:

| Field | Meaning |
|---|---|
| `schema_version` | Must be `1`. |
| `profile_revision` | Positive integer incremented whenever this profile changes. |
| `name` | Stable profile and filename identifier. |
| `description` | Human-readable summary. |
| `journal` | Forward the container's normal rsyslog/journal stream. |
| `required_paths` | Paths that must exist before installation. |
| `files` | Regular file/glob inputs. |
| `tasks` | File/glob inputs that may prepend the source filename. |
| `docker.enabled` | Discover Docker `json-file` paths at generation time. |
| `detect` | Safe declarative auto-detection probes. |
| `test_service` | APP-NAME used by the post-install test message. |
| `notes` | Operational caveats shown after install and during status. |

## Sources

```json
{
  "path": "/var/log/postgresql/postgresql-*-main.log",
  "service": "postgres",
  "required": true,
  "facility": "local5",
  "severity": "info",
  "include_filename": false
}
```

`facility`, `severity`, `required`, and `include_filename` are optional. `freshStartTail` is deliberately controlled by the engine, not profiles.

Task sources with `include_filename: true` prepend the basename in square brackets. This is how PBS keeps its UPID available in Loki without using it as a label.

## Detection

Detection supports four probe types:

- `path`: `test -e` inside the CT;
- `service`: `systemctl cat` inside the CT;
- `command`: shell `command -v` with the value passed as an argument;
- `package`: exact `dpkg-query -W` lookup.

`mode` is `any` or `all`. Matching profiles are ranked by `priority`, then by number of matched probes. An exact score tie is rejected as ambiguous instead of silently choosing a profile. The generic Docker profile has intentionally low priority.

Profiles with an empty probe list, such as `custom-app`, are explicit-only.

## Revision contract

`schema_version` versions the structure understood by the installer. `profile_revision` versions the operational content of one service profile and starts at 1. The generated rsyslog file records both the revision and the raw profile SHA-256.

Increase `profile_revision` for every intentional edit. If the content changes without a revision bump, inventory reports `hash drift at same revision` and synchronization still deploys the exact new content.

Managed files created before this field existed have a profile name and hash but no revision marker. They are interpreted as legacy revision 1; the first `--sync` writes the explicit marker.
