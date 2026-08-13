# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.3.0] - 2026-08-12

### Added

- Service profiles for Authentik, BookOrbit, Caddy, Deluge, Profilarr, Seerr, Tautulli, Transmission, and Wizarr.
- Vito application collection for Laravel, Horizon worker, and WebSocket logs.

### Changed

- Service detection now reflects observed saint-cluster units for BirdNET-Go, Syncthing, and UniFi OS.
- Beszel auto-detection now targets the hub only, preventing the widely deployed agent unit from claiming unrelated application containers.

## [1.2.3] - 2026-08-12

### Added

- Node installer/updater that discovers the latest GitHub release, preserves the site configuration, validates the downloaded release, and supports version-pinned rollback.

### Fixed

- The CLI now resolves its executable symlink before looking for bundled profiles and configuration.

## [1.2.2] - 2026-08-12

### Fixed

- Successful `pct` commands now keep stderr diagnostics separate from machine-readable stdout, preventing host locale warnings from corrupting Docker API version discovery.

## [1.2.1] - 2026-08-12

### Fixed

- GitHub Actions no longer requires the Debian-only `rsyslog-docker` package on Ubuntu runners; Docker API rendering remains covered by unit tests, while `rsyslogd` integration validation runs when the module is available.

## [1.2.0] - 2026-08-12

### Added

- Profile-driven Docker API collection through rsyslog `imdocker`, including automatic `rsyslog-docker` package installation, continuous container discovery, and container-name service labels.

### Changed

- The generic Docker profile now uses API discovery instead of static container-ID `json-file` mappings.

## [1.1.0] - 2026-08-12

### Added

- Go implementation of profile validation, service detection, rsyslog rendering, deployment, status, inventory, synchronization, and migration.
- Go tests covering deterministic rendering, detection, deployment, idempotency, rollback, inventory drift, and generated rsyslog syntax.
- GitHub Actions workflow that publishes Linux AMD64 archives when a numeric release tag matches `VERSION`.
- Simple versioned deployment and rollback instructions.

### Changed

- Build, test, validation, and packaging commands now use Go and the standard library.
- Release archives and directories use names such as `homelab-logging-1.1.0` without a `v` prefix.
- Continuous integration now formats, vets, tests, and builds the Go application.

### Fixed

- Rsyslog syntax tests now use an AppArmor-approved configuration path on Ubuntu CI runners.
- Docker profiles without discovered `json-file` mappings no longer generate an empty `imfile` pipeline that rsyslog rejects.

### Removed

- Bash installer, shell libraries, shell test harness, and the runtime dependency on `jq`.

[Unreleased]: https://github.com/jpmurray/homelab-logging/compare/1.3.0...HEAD
[1.3.0]: https://github.com/jpmurray/homelab-logging/compare/1.2.3...1.3.0
[1.2.3]: https://github.com/jpmurray/homelab-logging/compare/1.2.2...1.2.3
[1.2.2]: https://github.com/jpmurray/homelab-logging/compare/1.2.1...1.2.2
[1.2.1]: https://github.com/jpmurray/homelab-logging/compare/1.2.0...1.2.1
[1.2.0]: https://github.com/jpmurray/homelab-logging/compare/1.1.0...1.2.0
[1.1.0]: https://github.com/jpmurray/homelab-logging/compare/1.0.0...1.1.0
[1.0.0]: https://github.com/jpmurray/homelab-logging/releases/tag/1.0.0
