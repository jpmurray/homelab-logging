# homelab-logging

A small Go CLI for forwarding Proxmox VE host and LXC logs to Grafana Alloy over RFC5424 syslog. The same binary and profiles run on each Proxmox node; service behavior lives in JSON profiles and site-specific values live in `config.json`.

This is intentionally a personal homelab tool. It uses the Go standard library, calls the local `pct` command for containers, and keeps its state in the generated rsyslog file on each managed target.

## What it does

- Validates the site configuration and all service profiles.
- Installs, audits, inventories, and synchronizes logging on the local Proxmox VE host.
- Auto-detects profiles from paths, systemd services, commands, and packages.
- Installs rsyslog on a target host or LXC when necessary.
- Generates deterministic rsyslog configuration.
- Backs up the existing managed configuration.
- Disables only explicitly configured legacy forwarding files.
- Validates the complete rsyslog configuration before restarting.
- Rolls back managed and legacy files if deployment fails.
- Avoids restarts when the deployed configuration is byte-identical.
- Audits, inventories, synchronizes, and refreshes migrated LXCs.
- Continuously discovers Docker containers through the Docker API and labels them by container name.
- Sends `cluster`, `location`, `node`, `role`, and `job` as RFC5424 structured data.

## Requirements

The binary runs on a Proxmox VE host. LXC operations need `pct`. Write operations run as root; rsyslog is installed automatically when absent on both the host and Debian-family systemd containers.

Building from source requires Go 1.24 or newer:

```bash
make build
```

Before deploying hosts or LXCs, install `alloy/config.alloy` on the collector or merge the syslog contract from `alloy/syslog-labels.alloy`.

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

The Proxmox node label comes from the host's short hostname. `cluster` and `location` remain site-wide values. `origin_role` applies to LXCs; native host operations always emit `role=proxmox-host` to preserve the existing Alloy label contract.

## Usage

```bash
./homelab-logging --validate
./homelab-logging --list

./homelab-logging 100 pbs --dry-run
./homelab-logging 100 pbs
./homelab-logging 105
./homelab-logging 105 --status
./homelab-logging 105 --migrate

./homelab-logging --host --dry-run
./homelab-logging --host
./homelab-logging --host --status
./homelab-logging --host --inventory
./homelab-logging --host --sync --dry-run
./homelab-logging --host --sync

./homelab-logging --inventory
./homelab-logging --sync postgres --dry-run
./homelab-logging --sync postgres
./homelab-logging --sync
```

Use `--config PATH` or `--profiles-dir PATH` to override the files beside the binary.

## Proxmox host logging

The `pve-host` profile preserves the existing Saint-Cluster host sources: the normal journal/syslog stream, `/var/log/pveproxy/access.log`, and all `/var/log/pve/tasks/*/UPID:*` task logs. Task UPIDs remain in message bodies instead of becoming Loki labels.

The first `--host` installation treats paths in `legacy_configs`, including `/etc/rsyslog.d/90-alloy.conf`, as legacy forwarding. It preserves and disables the legacy file, validates the complete replacement configuration, and rolls back on failure. Subsequent releases are reconciled with `--host --sync`.

## Deployment

Create a GitHub release by updating `VERSION`, committing the change, and pushing a matching numeric tag:

```bash
git tag 1.4.1
git push origin 1.4.1
```

GitHub Actions tests the project and publishes `homelab-logging-1.4.1-linux-amd64.zip`.

On each Proxmox node, download and run the installer as root:

```bash
curl -fsSLO https://raw.githubusercontent.com/jpmurray/homelab-logging/main/install.sh
sudo bash install.sh
```

The installer finds the latest GitHub release, downloads its Linux AMD64 archive, validates it, and switches `/opt/homelab-logging/current` to the new release. It keeps the site configuration at `/opt/homelab-logging/config.json`, so updates do not replace local settings. Edit that file after the first install, then run:

```bash
homelab-logging --validate
homelab-logging --host --dry-run
homelab-logging --host
homelab-logging --sync --dry-run
homelab-logging --sync
```

To update to the latest release later:

```bash
sudo homelab-logging-update
```

Pass `--version 1.4.1` to install or roll back to a specific release. Previous releases remain under `/opt/homelab-logging/releases`.

## Profiles and detection

Profiles in `services/` declare journal, file, task, and Docker sources. Detection probes may check a path, systemd service, command, or Debian package.

Matching profiles are ranked by priority and matched probe count. An exact tie is reported as ambiguous. Profiles with no probes, such as `custom-app`, must be selected explicitly.

Each profile has a monotonically increasing `profile_revision`. The generated file records that revision plus exact profile and site SHA-256 hashes. Inventory reports revision changes, same-revision hash drift, stale node labels, and legacy installations that predate revision markers.

## Deployment and rollback

A host or LXC write operation:

1. verifies the target is available;
2. checks required paths and sources;
3. installs rsyslog and any profile-required input plugin when absent;
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

Docker records use `job=docker` and `service=<container_name>`. The generic Docker profile uses rsyslog's Docker API input, so new and recreated containers are picked up automatically without regenerating ID-specific file mappings. Task identifiers and filenames remain in message bodies instead of becoming high-cardinality Loki labels.

## Development

```bash
make test
make validate
make package
```

Tests use an in-memory fake Proxmox client for detection, deployment, idempotency, rollback, and inventory. When `rsyslogd` is installed, every generated profile is also syntax-checked. `make package` creates a Linux AMD64 binary and a self-contained zip under `dist/`.

See `CHANGELOG.md` for notable changes and `docs/ARCHITECTURE.md` and `docs/PROFILE_SCHEMA.md` for the internal contracts.
