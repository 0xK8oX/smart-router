#!/bin/bash
set -euo pipefail

# Daily backup of smart-router databases. Keep last 5 copies.
# Run manually or from cron/start.sh.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
DATA_DIR="${PROJECT_DIR}/data"
BACKUP_DIR="${DATA_DIR}/backups"
DATE=$(date +%Y-%m-%d)
KEEP=5

mkdir -p "${BACKUP_DIR}/db"
mkdir -p "${BACKUP_DIR}/health"

# --- SQLite backup (consistent snapshot without WAL deps) ---
DB_SRC="${DATA_DIR}/smart-router.db"
DB_DST_DIR="${BACKUP_DIR}/db/${DATE}"

if [ -f "$DB_SRC" ]; then
  mkdir -p "$DB_DST_DIR"
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$DB_SRC" ".backup '${DB_DST_DIR}/smart-router.db'"
  else
    # Fallback: copy all WAL files together
    cp "${DB_SRC}"* "${DB_DST_DIR}/"
  fi
  echo "[backup] SQLite -> ${DB_DST_DIR}"
fi

# --- Health DB backup (BadgerDB directory) ---
HEALTH_SRC="${DATA_DIR}/health"
HEALTH_DST="${BACKUP_DIR}/health/${DATE}"

if [ -d "$HEALTH_SRC" ]; then
  mkdir -p "$HEALTH_DST"
  cp -r "${HEALTH_SRC}"/* "${HEALTH_DST}/"
  echo "[backup] Health   -> ${HEALTH_DST}"
fi

# --- Rotate: keep only last $KEEP backups ---
rotate() {
  local dir=$1
  if [ -d "$dir" ]; then
    ls -1t "$dir" | tail -n +$((KEEP + 1)) | while read -r d; do
      rm -rf "${dir}/${d}"
      echo "[backup] removed old ${dir}/${d}"
    done
  fi
}

rotate "${BACKUP_DIR}/db"
rotate "${BACKUP_DIR}/health"
