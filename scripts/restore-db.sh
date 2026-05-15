#!/usr/bin/env bash
# Restore a SQLite backup over a (presumed-stopped) mqConnector database.
#
# Usage:
#   ./scripts/restore-db.sh ./backups/20260515T103000Z.db
#   DB_PATH=/var/lib/mqc/mqconnector.db ./scripts/restore-db.sh path/to/backup.db
#
# Safety:
#   - Refuses to run if the source database is being modified (busy lock test).
#   - Snapshots the current database to <DB_PATH>.pre-restore.<ts> before
#     overwriting, so an operator-error restore is reversible.
#   - Verifies integrity of the backup BEFORE touching the live file.
#   - Operator is expected to stop the mqconnector process first; this script
#     does not assume anything about systemd/docker/whatever orchestrator
#     wraps the binary.
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: $0 <backup-file>" >&2
    exit 2
fi
SRC="$1"
DB_PATH="${DB_PATH:-./data/mqconnector.db}"

if [[ ! -f "$SRC" ]]; then
    echo "error: backup file not found: $SRC" >&2
    exit 1
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "error: sqlite3 not found in PATH" >&2
    exit 1
fi

echo "verifying backup integrity..."
RESULT="$(sqlite3 "$SRC" 'PRAGMA integrity_check;')"
if [[ "$RESULT" != "ok" ]]; then
    echo "error: backup failed integrity_check: $RESULT" >&2
    exit 1
fi

# Refuse to restore over a live database. The cheapest "is anyone holding
# this open?" test is to try an exclusive begin/rollback and see if it
# returns immediately. Busy = a writer is around = bail out.
if [[ -f "$DB_PATH" ]]; then
    if ! sqlite3 "$DB_PATH" 'BEGIN EXCLUSIVE; ROLLBACK;' 2>/dev/null; then
        echo "error: $DB_PATH appears to be in use. Stop mqconnector first." >&2
        exit 1
    fi
    TS="$(date -u +%Y%m%dT%H%M%SZ)"
    PRE="${DB_PATH}.pre-restore.${TS}"
    cp -a "$DB_PATH" "$PRE"
    # Copy the journal/wal/shm too if present, so the rollback target is
    # internally consistent.
    for ext in -journal -wal -shm; do
        [[ -f "${DB_PATH}${ext}" ]] && cp -a "${DB_PATH}${ext}" "${PRE}${ext}"
    done
    echo "snapshotted current db -> $PRE"
fi

mkdir -p "$(dirname "$DB_PATH")"
cp -a "$SRC" "$DB_PATH"

# Drop WAL/SHM left from the previous instance — the restored file is the
# new source of truth and they would otherwise reapply stale frames.
for ext in -wal -shm -journal; do
    rm -f "${DB_PATH}${ext}"
done

echo "restored $SRC -> $DB_PATH"
if [[ -n "${PRE:-}" ]]; then
    echo "start mqconnector to verify; rollback path: $PRE"
else
    echo "start mqconnector to verify (no prior database to roll back to)"
fi
