#!/usr/bin/env bash
# Snapshot wallet + generator SQLite files and a permissioned copy of the
# secrets directory. Never prints secret contents.
set -euo pipefail

ROOT="${INSTALL_DIR:-/opt/bsv-noise}"
STAMP="$(date +%Y%m%d-%H%M%S)"
DEST="${1:-/var/backups/bsv-noise-${STAMP}}"

if [[ ! -d "$ROOT/data" && ! -d "$ROOT/secrets" ]]; then
  echo "error: expected data/ and/or secrets/ under $ROOT" >&2
  exit 1
fi

mkdir -p "$DEST"
chmod 700 "$DEST"

if [[ -d "$ROOT/data" ]]; then
  # Consistent-enough copy for a stopped or lightly loaded SQLite set.
  # For a perfectly consistent backup, stop the container first:
  #   docker compose -f "$ROOT/docker-compose.yml" stop
  cp -a "$ROOT/data" "$DEST/data"
fi

if [[ -d "$ROOT/secrets" ]]; then
  mkdir -p "$DEST/secrets"
  chmod 700 "$DEST/secrets"
  # Copy without reading contents into the shell.
  cp -a "$ROOT/secrets/." "$DEST/secrets/"
  chmod 600 "$DEST/secrets/"* 2>/dev/null || true
fi

if [[ -f "$ROOT/.env" ]]; then
  cp -a "$ROOT/.env" "$DEST/.env"
  chmod 600 "$DEST/.env"
fi

if [[ -f "$ROOT/docker-compose.yml" ]]; then
  cp -a "$ROOT/docker-compose.yml" "$DEST/docker-compose.yml"
fi

# Record the running image id if Docker is available, for rollback.
if command -v docker >/dev/null 2>&1; then
  docker image inspect bsv-noise:local --format '{{.Id}} {{.Created}}' >"$DEST/image-id.txt" 2>/dev/null || true
  docker compose -f "$ROOT/docker-compose.yml" --project-directory "$ROOT" ps >"$DEST/compose-ps.txt" 2>/dev/null || true
fi

echo "backup written to $DEST"
echo "restore: stop the service, copy data/ and secrets/ back, then start."
echo "rollback image: docker tag <previous-id> bsv-noise:local && docker compose up -d"
