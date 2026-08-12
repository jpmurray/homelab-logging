# homelab-logging v1

A small, host-agnostic installer for forwarding Proxmox LXC logs to Grafana Alloy over RFC5424 syslog. The installer code is shared by every Proxmox host; service behavior lives in validated JSON profiles and site-specific values live in `config.json`.

## What v1 does

- Validates a stable, declarative profile schema.
- Auto-detects profiles from paths, systemd services, commands, and packages.
- Installs rsyslog in the target LXC when necessary.
- Generates deterministic rsyslog configuration from the selected profile.
- Backs up the existing managed configuration before changing it.
- Detects configured legacy rsyslog files, preserves them under a non-`.conf` name, and deactivates them to prevent duplicate forwarding.
- Restores both managed and legacy configurations if deployment fails.
- Validates the complete rsyslog configuration before restarting.
- Rolls back automatically if validation, enablement, restart, or health checks fail.
- Avoids restarts when the generated and deployed configurations already match.
- Supports `--dry-run`, `--status`, `--inventory`, `--sync`, `--migrate`, `--list`, and `--validate`.
- Tracks a human-readable profile revision and an exact SHA-256 in every managed LXC.
- Collects generic Docker `json-file` logs and derives `service` from container names.
- Sends `cluster`, `location`, `node`, `role`, and `job` as RFC5424 structured data so Alloy preserves the datacenter → Proxmox node → LXC hierarchy.

## Requirements

Run the tool as root on a Proxmox VE host. The host needs Bash, `pct`, and `jq`. Target containers must be Debian-family systemd containers with APT; rsyslog is installed automatically when absent.

Before deploying LXCs, install the complete Saint-Cluster Alloy configuration from [`alloy/config.alloy`](alloy/config.alloy), or merge only the syslog contract from [`alloy/syslog-labels.alloy`](alloy/syslog-labels.alloy) when the collector has additional components. The complete file preserves local journal collection, defines the `local` Loki writer, and contains exactly one TCP/1514 listener. Without the relabeling contract, messages still arrive but their origin labels are not promoted correctly.

## Site configuration

Edit `config.json` once per logical site:

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

The same repository and installer can be copied to every Proxmox host. A new host needs no Bash changes. The installer derives the `node` label from the current Proxmox host's short hostname (`hostname -s`), while `cluster` and `location` remain site-wide values. Add any older installer-owned rsyslog paths to `legacy_configs`; the default covers the earlier `/etc/rsyslog.d/90-alloy.conf` setup.

## Usage

```bash
./install.sh --validate
./install.sh --list

./install.sh 100 pbs --dry-run
./install.sh 100 pbs

./install.sh 105             # auto-detects postgres
./install.sh 105 --status
./install.sh 105 --migrate   # refresh after Proxmox moved CT 105 to this node

./install.sh --inventory
./install.sh --sync postgres --dry-run
./install.sh --sync postgres
./install.sh --sync          # reconcile every managed profile on this node
```

`--status` checks:

- rsyslog installation and active state;
- installed profile and Proxmox node identity;
- exact generated/deployed configuration equality;
- Alloy TCP reachability from the LXC;
- required and optional file/task sources;
- current Docker container-to-log mappings;
- whether an earlier rsyslog forwarding configuration is still active.

A healthy audit exits 0. Drift or an unhealthy required component exits nonzero. If an LXC was moved to another node, status reports the stale node and recommends `--migrate`.

## Profile revisions and synchronization

Each service file has a monotonically increasing `profile_revision`. Increment it whenever that profile changes. The deployed configuration records the profile name, revision, and exact JSON SHA-256. The revision makes inventory readable; the hash remains authoritative and detects a changed JSON file when its revision was not bumped.

```bash
./install.sh --inventory
./install.sh --inventory beszel
./install.sh --sync beszel --dry-run
./install.sh --sync beszel
```

`--inventory` scans running LXCs on the current Proxmox node and reports installed versus available revisions. `--sync` selects each LXC by its recorded profile name—never by auto-detection—and runs the normal backup, validation, restart, health check, and rollback transaction independently. One failure does not prevent the remaining matching LXCs from being attempted.

Configurations installed before profile revisions existed are recognized from their existing `homelab-logging-profile` marker and inferred as revision 1. Their first successful sync stamps the explicit revision marker. No manual reinstallation or inventory database is required.

Stopped LXCs cannot be safely inspected through `pct exec`; they are reported as stopped/uninspected and are discovered from their existing configuration the next time they are running. Run synchronization once per Proxmox node because `pct list` is node-local.

## LXC migration workflow

Move the LXC with Proxmox first. Then, from this repository on the destination node, refresh its logging configuration:

```bash
./install.sh 105 --migrate --dry-run
./install.sh 105 --migrate
./install.sh 105 --status
```

`--migrate` does not move the LXC. It reads the profile already recorded in the managed rsyslog configuration, regenerates that configuration with the destination node's hostname, refreshes Docker container mappings when applicable, and uses the same backup, validation, and rollback transaction as a normal installation. You do not need to specify the profile again.

## Saint-Cluster coverage

The `services/` directory contains reusable profiles for:

- PBS, PostgreSQL, MariaDB, Apt-Cacher-NG, Technitium, Plex, Beszel, qBittorrent, CouchDB, and a custom app;
- Docker LXCs, including Mealie, Seerr, Tailscale IdP, Papra, Invoice Ninja, Audiobookshelf, and Forgejo Runner without application-specific profiles;
- BirdNET-Go, Loki, Forgejo, Grafana, Prowlarr, Radarr, Sonarr, Vito, Syncthing, PeaNUT, and UniFi OS.

Unverified application log files are intentionally not guessed. A journal-only profile is a complete and conservative v1 profile.

### PBS policy

PBS collects its journal, API access/auth logs, and these task types:

- `backup`
- `prunejob`
- `garbage_collection`
- `syncjob`

It deliberately excludes `verificationjob`: the sampled Saint-Cluster inventory found only seven verification task files consuming roughly 985 MB. Low-value administrative task types are also excluded. Task UPIDs stay in the message body instead of becoming high-cardinality Loki labels.

### Docker policy

The Docker profile reads each container's `LogPath` returned by `docker inspect` and uses the container name as `service`. This supports Docker's `json-file` driver without changing Docker daemon or Compose configuration.

Run `./install.sh --sync docker` after adding, deleting, or renaming containers. The status command reports drift when the generated mappings differ. Containers using `journald`, `local`, `syslog`, or another driver are skipped because they have no `json-file` path.

## Expected labels

An LXC PostgreSQL record should arrive as:

```text
cluster=saint-cluster
location=home
node=patate-du-cluster
host=postgres
role=lxc
job=syslog
service=postgres
```

Docker records use the same LXC `node` and `host`, with `job=docker` and `service=<container_name>`. Thus Docker-in-LXC remains attributable to both its container and its enclosing LXC/Proxmox node.

## Grafana dashboard

Import `grafana/dashboards/saint-cluster-logging.json` from **Dashboards → New → Import** and select the Loki data source when prompted. The dashboard provides:

- cascading `location → cluster → node → host → role → job → service` filters;
- total log, active node/host, and error-like line statistics;
- log and error rates grouped by topology;
- a label-audit table and busiest-source view;
- a searchable logs panel.

The dashboard is portable: it references the import-time `DS_LOKI` input instead of embedding a Grafana-specific data-source UID.

## Safety model

The installer owns the configured managed file and only the explicitly listed `legacy_configs` under `/etc/rsyslog.d/`. An active legacy file is atomically renamed to `<path>.disabled.<timestamp>-<pid>`, which preserves its contents while moving it outside rsyslog's `*.conf` include pattern. It does not scan or disable unrelated rsyslog files, remove old collectors, alter application settings, change Docker's logging driver, or import historical files. `freshStartTail="on"` starts newly discovered file inputs at the tail on first activation; rsyslog then persists normal offsets.

Managed-file backups and disabled legacy files are stored next to their originals with unique timestamps. They are kept after successful deployment for manual recovery. A failed deployment automatically restores the previous managed file and reactivates every legacy file moved during that transaction.

## Development

```bash
make test
make package
```

The test suite uses a fake `pct` implementation to exercise detection, generation, idempotency, status, and rollback without a Proxmox host.
When `rsyslogd` is available, `tests/validate-rsyslog.sh` also validates a generated configuration for every profile. CI installs rsyslog and runs that check.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and [`docs/PROFILE_SCHEMA.md`](docs/PROFILE_SCHEMA.md) for design details.
