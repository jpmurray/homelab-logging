# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Go implementation of profile validation, service detection, rsyslog rendering, deployment, status, inventory, synchronization, and migration.
- Go tests covering deterministic rendering, detection, deployment, idempotency, rollback, inventory drift, and generated rsyslog syntax.
- GitHub Actions workflow that publishes Linux AMD64 archives when a numeric release tag matches `VERSION`.
- Simple versioned deployment and rollback instructions.

### Changed

- Build, test, validation, and packaging commands now use Go and the standard library.
- Release archives and directories use names such as `homelab-logging-1.4.0` without a `v` prefix.
- Continuous integration now formats, vets, tests, and builds the Go application.

### Fixed

- Rsyslog syntax tests now use an AppArmor-approved configuration path on Ubuntu CI runners.
- Docker profiles without discovered `json-file` mappings no longer generate an empty `imfile` pipeline that rsyslog rejects.

### Removed

- Bash installer, shell libraries, shell test harness, and the runtime dependency on `jq`.
