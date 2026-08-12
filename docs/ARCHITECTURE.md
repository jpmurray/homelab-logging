# Architecture

## Data path

```text
LXC journal/syslog ─┐
application files ──┼─> rsyslog RFC5424/TCP ─> Alloy :1514 ─> Loki
selected task logs ─┤
Docker json files ──┘
```

Rsyslog is the only transport inside a target LXC. This avoids collecting the same journal twice. File and task inputs use dedicated rulesets ending in `stop`, so they do not fall through into the journal forwarding action.

Each forwarding action uses a disk-assisted linked-list queue with infinite retry. Source offsets are owned by rsyslog. First activation tails existing files instead of importing history. This deliberately accepts the small `freshStartTail` startup race in exchange for preventing a large historical import.

## Separation of concerns

- `install.sh` owns CLI orchestration, audit, safe deployment, and rollback.
- `lib/profile.sh` owns schema enforcement and detection.
- `lib/rsyslog.sh` turns declarative inputs into rsyslog configuration.
- `lib/pct.sh` is the Proxmox transport boundary.
- `config.json` contains site identity and Alloy destination; the runtime derives the Proxmox node from `hostname -s`.
- `services/*.json` contain application facts and a monotonically increasing profile revision.

A new application normally requires one JSON file. A new Proxmox host requires no engine changes.

## Label contract

Rsyslog emits RFC5424 structured data using enterprise ID `homelab@32473`:

```text
[homelab@32473 cluster="saint-cluster" location="home" role="lxc" node="patate-du-cluster" job="syslog"]
```

Alloy exposes these as internal labels such as `__syslog_message_sd_homelab_32473_role`. The supplied relabel rules promote them before Alloy removes internal labels. The RFC5424 hostname becomes `host`, APP-NAME becomes `service`, and structured `node` records the Proxmox host that installed or most recently refreshed the LXC configuration.

This solves the shared-listener problem: messages from LXC, Proxmox-host, VPS, and Docker origins can use the same TCP port while retaining correct roles. Existing unstructured Proxmox host traffic receives conservative fallback labels until its sender configuration is updated to emit the same structured-data contract. For that traffic, Alloy temporarily derives both `host` and `node` from the syslog hostname.

UPIDs and filenames are kept in the log body rather than labels. This keeps useful correlation context without creating a Loki stream for every task.

## Deployment transaction

1. Validate the site file and profile.
2. Confirm the CT exists and is running.
3. Confirm required paths and sources.
4. Install rsyslog if absent.
5. Generate a deterministic local candidate.
6. Return early only if the deployed file is byte-identical and no configured legacy file remains active.
7. Back up the managed remote file.
8. Atomically rename active legacy configurations to non-`.conf` disabled backups.
9. Push the candidate.
10. Run `rsyslogd -N1` against the complete container configuration.
11. Enable, restart, and verify rsyslog.
12. Emit a harmless test message.

Any failure after deployment starts restores the managed backup (or removes a first-time candidate), moves every disabled legacy file back to its original active path, and restarts the previous configuration.

## Inventory and reconciliation

The managed rsyslog file is the distributed source of truth. It records profile name, profile revision, profile hash, site hash, and Proxmox node. `--inventory` scans running local LXCs for those markers. A missing revision marker with a valid profile marker denotes a pre-revision installation and is inferred as revision 1.

`--sync [PROFILE]` reuses the recorded profile rather than auto-detecting. Each matching LXC is reconciled in a separate installer process so its lock, backup, rollback state, and failure are isolated from the rest of the batch. Exact generated-file comparison makes the operation idempotent and also covers engine, site, node, and dynamic Docker mapping changes.

Discovery is node-local and stopped LXCs are reported but not mounted or modified. This avoids changing storage state merely to perform an audit.

## Proxmox node migration

The installed file records both the selected profile and the Proxmox node. After Proxmox moves an LXC, `./install.sh <CTID> --migrate` runs on the destination node. It reuses the recorded profile and the normal deployment transaction, replaces the stale `node` structured-data value with the destination host's short hostname, and refreshes dynamic Docker mappings. `--status` detects a stale installed node and points to this command. The command refreshes logging metadata only; Proxmox remains responsible for moving the LXC.

## Non-goals for v1

- Managing Alloy or Loki deployment itself.
- Removing the old OpenTelemetry collector.
- Parsing every application-specific file format.
- Turning task IDs, users, VMIDs, or Docker IDs into Loki labels.
- Changing Docker daemon logging settings.
- Supporting non-systemd or non-Debian LXCs.
