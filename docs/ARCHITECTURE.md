# Architecture

## Data path

```text
LXC journal/syslog ─┐
application files ──┼─> rsyslog RFC5424/TCP ─> Alloy :1514 ─> Loki
selected task logs ─┤
Docker json files ──┘
```

Rsyslog is the only transport inside a target LXC. File and task inputs use dedicated rulesets ending in `stop` so they do not fall through into journal forwarding. Disk-assisted queues retry indefinitely, and rsyslog owns source offsets. New file inputs begin at the tail.

## Program structure

The Go implementation is intentionally flat:

- `main.go` parses the CLI and loads repository configuration.
- `model.go` defines and validates site and profile JSON.
- `pct.go` is the small Proxmox command boundary.
- `rsyslog.go` deterministically renders RainerScript.
- `app.go` contains detection, audit, reconciliation, deployment, and rollback.
- `app_test.go` exercises behavior with an in-memory fake client.

There is no database. The managed rsyslog file inside each LXC is the distributed source of truth.

## Label contract

Rsyslog emits RFC5424 structured data using enterprise ID `homelab@32473`:

```text
[homelab@32473 cluster="saint-cluster" location="home" role="lxc" node="patate-du-cluster" job="syslog"]
```

Alloy promotes these structured values to Loki labels. The RFC5424 hostname becomes `host` and APP-NAME becomes `service`. Task IDs stay in the message body.

## Deployment transaction

Each CT operation owns its lock and rollback state:

1. validate local configuration and the selected profile;
2. verify the CT and required sources;
3. install rsyslog if needed;
4. generate a deterministic candidate;
5. compare it byte-for-byte with the deployed file;
6. back up the managed file and disable configured legacy files;
7. push and validate the candidate;
8. enable, restart, and health-check rsyslog;
9. commit the transaction and emit a test message.

Any error after mutation begins restores the previous managed file and every legacy file moved by that transaction.

## Inventory, synchronization, and migration

Managed files record profile name, revision, hashes, and Proxmox node. Inventory reads those markers from running local LXCs. Synchronization selects by the recorded profile and reconciles each CT independently; one failure does not stop the remaining CTs.

Stopped LXCs are reported but not mounted or modified. After Proxmox moves an LXC, `--migrate` regenerates its configuration with the destination node label and current Docker mappings.

## Deliberate limits

- Debian-family, systemd-based LXCs only.
- Proxmox `pct` is the only container transport.
- Alloy and Loki deployment remain external.
- No historical file import.
- No automatic modification of Docker logging drivers.
- No central inventory service.
