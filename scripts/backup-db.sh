#!/usr/bin/env bash
# Atomic SQLite backup using the SQLite Online Backup API via the `sqlite3`
# shell. This is safe to run against a live database: SQLite holds a brief
# read lock during the copy and writers are not blocked.
#
# Usage:
#   ./scripts/backup-db.sh                    # writes to ./backups/<ts>.db
#   ./scripts/backup-db.sh /path/to/out.db    # explicit output path
#   DB_PATH=/var/lib/mqc/mqconnector.db \
#     ./scripts/backup-db.sh                  # override source
#
# Verifies the backup integrity with `PRAGMA integrity_check` before
# reporting success. Exits non-zero on any failure.
set -euo pipefail

DB_PATH="${DB_PATH:-./data/mqconnector.db}"
if [[ ! -f "$DB_PATH" ]]; then
    echo "error: source database not found: $DB_PATH" >&2
    exit 1
fi

OUT="${1:-}"
if [[ -z "$OUT" ]]; then
    mkdir -p ./backups
    OUT="./backups/$(date -u +%Y%m%dT%H%M%SZ).db"
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "error: sqlite3 not found in PATH (apt install sqlite3 / brew install sqlite)" >&2
    exit 1
fi

# .backup uses the online backup API. Tempfile + rename for atomicity so
# a crash mid-copy never leaves a half-written file at the final path.
TMP="${OUT}.partial"
trap 'rm -f "$TMP"' EXIT
sqlite3 "$DB_PATH" ".backup '$TMP'"

# Independent integrity check on the copy.
RESULT="$(sqlite3 "$TMP" 'PRAGMA integrity_check;')"
if [[ "$RESULT" != "ok" ]]; then
    echo "error: integrity_check failed on backup: $RESULT" >&2
    exit 1
fi

mv -f "$TMP" "$OUT"
trap - EXIT

SIZE="$(wc -c <"$OUT" | tr -d ' ')"
echo "backed up $DB_PATH -> $OUT ($SIZE bytes, integrity ok)"
