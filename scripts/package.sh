#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
DIST="$ROOT/dist"
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/homelab-logging-package.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT

mkdir -p "$DIST" "$STAGE/homelab-logging-v$VERSION"
for item in VERSION README.md LICENSE config.json install.sh Makefile alloy docs grafana lib schema services scripts tests; do
    cp -R "$ROOT/$item" "$STAGE/homelab-logging-v$VERSION/"
done
rm -rf "$STAGE/homelab-logging-v$VERSION/dist"
(cd "$STAGE" && zip -qr "$DIST/homelab-logging-v$VERSION.zip" "homelab-logging-v$VERSION")
printf '%s\n' "$DIST/homelab-logging-v$VERSION.zip"
