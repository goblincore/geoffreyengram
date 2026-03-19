#!/usr/bin/env bash
# backup-dualmem.sh — Safe backup of dualmem SQLite database
# Uses SQLite's .backup command for consistent snapshots (safe during writes).
# Keeps the last N backups and removes older ones.
#
# Usage:
#   ./scripts/backup-dualmem.sh                  # defaults
#   ./scripts/backup-dualmem.sh -d /path/to/db   # custom DB path
#   ./scripts/backup-dualmem.sh -o /backups       # custom output dir
#   ./scripts/backup-dualmem.sh -k 30             # keep 30 backups
#
# Cron example (daily at 2am):
#   0 2 * * * /path/to/scripts/backup-dualmem.sh >> /tmp/dualmem-backup.log 2>&1

set -euo pipefail

# --- Defaults ---
DB_PATH="${HOME}/.local/share/dualmem/memories.db"
BACKUP_DIR="${HOME}/.local/share/dualmem/backups"
KEEP=14  # number of backups to retain

# --- Parse flags ---
while getopts "d:o:k:h" opt; do
  case "$opt" in
    d) DB_PATH="$OPTARG" ;;
    o) BACKUP_DIR="$OPTARG" ;;
    k) KEEP="$OPTARG" ;;
    h)
      echo "Usage: $0 [-d db_path] [-o backup_dir] [-k keep_count]"
      exit 0
      ;;
    *) exit 1 ;;
  esac
done

# --- Validate ---
if [[ ! -f "$DB_PATH" ]]; then
  echo "ERROR: database not found: $DB_PATH" >&2
  exit 1
fi

# Prefer sqlite3 CLI for .backup (hot-safe), fall back to cp
HAS_SQLITE3=false
if command -v sqlite3 &>/dev/null; then
  HAS_SQLITE3=true
fi

# --- Setup ---
mkdir -p "$BACKUP_DIR"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="${BACKUP_DIR}/dualmem-${TIMESTAMP}.db"

# --- Backup ---
echo "[$(date -Iseconds)] Starting backup..."
echo "  Source: $DB_PATH"
echo "  Target: $BACKUP_FILE"

if $HAS_SQLITE3; then
  # sqlite3 .backup is atomic and safe even if the DB is being written to
  sqlite3 "$DB_PATH" ".backup '${BACKUP_FILE}'"
else
  echo "  WARN: sqlite3 not found, using file copy (ensure no active writes)"
  cp "$DB_PATH" "$BACKUP_FILE"
  # Also grab WAL/SHM if they exist
  [[ -f "${DB_PATH}-wal" ]] && cp "${DB_PATH}-wal" "${BACKUP_FILE}-wal"
  [[ -f "${DB_PATH}-shm" ]] && cp "${DB_PATH}-shm" "${BACKUP_FILE}-shm"
fi

# --- Verify ---
SIZE=$(stat -f%z "$BACKUP_FILE" 2>/dev/null || stat -c%s "$BACKUP_FILE" 2>/dev/null)
if [[ "$SIZE" -lt 1024 ]]; then
  echo "  WARN: backup is suspiciously small (${SIZE} bytes)" >&2
fi

if $HAS_SQLITE3; then
  INTEGRITY=$(sqlite3 "$BACKUP_FILE" "PRAGMA integrity_check;" 2>&1)
  if [[ "$INTEGRITY" != "ok" ]]; then
    echo "  ERROR: integrity check failed: $INTEGRITY" >&2
    exit 1
  fi
  echo "  Integrity: ok"
fi

echo "  Size: ${SIZE} bytes"

# --- Rotate old backups ---
BACKUP_COUNT=$(find "$BACKUP_DIR" -name 'dualmem-*.db' -maxdepth 1 | wc -l | tr -d ' ')
if [[ "$BACKUP_COUNT" -gt "$KEEP" ]]; then
  REMOVE_COUNT=$((BACKUP_COUNT - KEEP))
  echo "  Rotating: removing ${REMOVE_COUNT} old backup(s) (keeping ${KEEP})"
  # Remove oldest first (sorted by name = sorted by timestamp)
  find "$BACKUP_DIR" -name 'dualmem-*.db' -maxdepth 1 | sort | head -n "$REMOVE_COUNT" | while read -r f; do
    rm -f "$f" "${f}-wal" "${f}-shm"
  done
fi

echo "[$(date -Iseconds)] Backup complete: $BACKUP_FILE"
