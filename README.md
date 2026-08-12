# homelab-logging

A small Go CLI for forwarding Proxmox LXC logs to Grafana Alloy over RFC5424 syslog. The same binary and profiles run on each Proxmox node; service behavior lives in JSON profiles and site-specific values live in `config.json`.

This is intentionally a personal homelab tool. It uses the Go standard library, calls the local `pct` command, and keeps its state in the generated rsyslog file inside each managed LXC.

## What it does

- Validates the site configuration and all service profiles.
- Auto-detects profiles from paths, systemd services, commands, and packages.
- Installs rsyslog in a target LXC when necessary.
- Generates deterministic rsyslog configuration.
- Backs up the existing managed configuration.
- Disables only explicitly configured legacy forwarding files.
- Validates the complete rsyslog configuration before restarting.
- Rolls back managed and legacy files if deployment fails.
- Avoids restarts when the deployed configuration is byte-identical.
- Audits, inventories, synchronizes, and refreshes migrated LXCs.
- Discovers Docker `json-file` paths and labels them by container name.
- Sends `cluster`, `location`, `node`, `role`, and `job` as RFC5424 structured data.

## Requirements

The binary runs as root on a Proxmox VE host. The host needs `pct`. Target containers must be Debian-family systemd containers with APT; rsyslog is installed automatically when absent.

Building from source requires Go 1.24 or newer:

```bash
make build
```

Before deploying LXCs, install `alloy/config.alloy` on the collector or merge the syslog contract from `alloy/syslog-labels.alloy`.

## Site configuration

Edit `config.json` once for the site:

```json
{
  "schema_version": 1,
  "cluster": "saint-cluster",
  "location": "home",
  "origin_role": "lxc",
  "alloy": {
    "host": "100.90.189.45",
    "port": 1514,
    "protocol": "tcp"
  },
  "remote_config": "/etc/rsyslog.d/90-homelab-alloy.conf",
  "legacy_configs": [
    "/etc/rsyslog.d/90-alloy.conf"
  ]
}
```

The Proxmox node label comes from the host's short hostname. `cluster` and `location` remain site-wide values.

## Usage

```bash
./homelab-logging --validate
./homelab-logging --list

./homelab-logging 100 pbs --dry-run
./homelab-logging 100 pbs
./homelab-logging 105
./homelab-logging 105 --status
./homelab-logging 105 --migrate

./homelab-logging --inventory
./homelab-logging --sync postgres --dry-run
./homelab-logging --sync postgres
./homelab-logging --sync
```

Use `--config PATH` or `--profiles-dir PATH` to override the files beside the binary.

## Deployment

Create a GitHub release by updating `VERSION`, committing the change, and pushing a matching numeric tag:

```bash
git tag 1.1.0
git push origin 1.1.0
```

GitHub Actions tests the project and publishes `homelab-logging-1.1.0-linux-amd64.zip`. Download that archive from the repository's Releases page and copy it to each Proxmox node.

On a node, keep releases under `/opt` and point `current` at the active one:

```bash
mkdir -p /opt/homelab-logging/releases
unzip homelab-logging-1.1.0-linux-amd64.zip -d /opt/homelab-logging/releases
ln -sfn /opt/homelab-logging/releases/homelab-logging-1.1.0 /opt/homelab-logging/current
ln -sfn /opt/homelab-logging/current/homelab-logging /usr/local/bin/homelab-logging

homelab-logging --validate
homelab-logging --sync --dry-run
homelab-logging --sync
```

To update, repeat those steps with the new release number. To roll back, point `current` at the previous release.

## Profiles and detection

Profiles in `services/` declare journal, file, task, and Docker sources. Detection probes may check a path, systemd service, command, or Debian package.

Matching profiles are ranked by priority and matched probe count. An exact tie is reported as ambiguous. Profiles with no probes, such as `custom-app`, must be selected explicitly.

Each profile has a monotonically increasing `profile_revision`. The generated file records that revision plus exact profile and site SHA-256 hashes. Inventory reports revision changes, same-revision hash drift, stale node labels, and legacy installations that predate revision markers.

## Deployment and rollback

A write operation:

1. verifies the CT exists and is running;
2. checks required paths and sources;
3. installs rsyslog when absent;
4. generates the candidate;
5. returns early when the deployed file is identical and no legacy file is active;
6. backs up the current managed file;
7. renames configured legacy files outside the `*.conf` include pattern;
8. pushes and validates the candidate with `rsyslogd -N1`;
9. enables, restarts, and checks rsyslog;
10. sends a harmless test message.

A failure restores the previous managed file, reactivates legacy files moved by that transaction, and restarts the old configuration. Backups remain beside the managed file for manual recovery.

## Labels

An LXC PostgreSQL record arrives with:

```text
cluster=saint-cluster
location=home
node=patate-du-cluster
host=postgres
role=lxc
job=syslog
service=postgres
```

Docker records use `job=docker` and `service=<container_name>`. Task identifiers and filenames remain in message bodies instead of becoming high-cardinality Loki labels.

## Development

```bash
make test
make validate
make package
```

Tests use an in-memory fake Proxmox client for detection, deployment, idempotency, rollback, and inventory. When `rsyslogd` is installed, every generated profile is also syntax-checked. `make package` creates a Linux AMD64 binary and a self-contained zip under `dist/`.

See `CHANGELOG.md` for notable changes and `docs/ARCHITECTURE.md` and `docs/PROFILE_SCHEMA.md` for the internal contracts.
