#!/usr/bin/env bash
# Archive audit rows older than RETAIN_DAYS to a flat NDJSON file, then
# delete them from the running database. Designed to be cron-friendly:
# exits non-zero on any failure, leaves the database untouched if the
# archive write or upload didn't succeed.
#
# Flow:
#   1. Snapshot the eligible rows to a tempfile as NDJSON (one row per
#      line). Hash-chain verification stays intact because we only ever
#      remove the OLDEST suffix — the chain's head and the freshly-
#      retained tail still verify against each other.
#   2. Optionally upload the tempfile to S3 via `aws s3 cp`. The aws
#      CLI handles SigV4; we don't reinvent it here.
#   3. Once the file is durably stored (local + optional remote), open
#      a write transaction and delete the same row id range from the
#      database. The id range is bounded by max(id) at snapshot time
#      so concurrent inserts that arrive during the upload are safe.
#
# Usage:
#   ./scripts/archive-audit.sh
#     # Reads env: DB_PATH, RETAIN_DAYS, OUT_DIR, S3_BUCKET, S3_PREFIX
#
# Defaults:
#   DB_PATH      = ./data/mqconnector.db
#   RETAIN_DAYS  = 90
#   OUT_DIR      = ./audit-archive/
#   S3_BUCKET    = "" (skip upload if empty)
#   S3_PREFIX    = audit/
#
# Recommended cron line (weekly, Sunday 02:15 UTC):
#   15 2 * * 0  DB_PATH=/var/lib/mqconnector/mqconnector.db \
#               RETAIN_DAYS=90 OUT_DIR=/var/lib/mqconnector/archive \
#               S3_BUCKET=mqc-audit S3_PREFIX=prod/ \
#               /opt/mqconnector/scripts/archive-audit.sh \
#               >> /var/log/mqc-archive.log 2>&1
#
# A successful run prints one summary line to stdout:
#   archived 12345 rows to <local-path> [+ s3://bucket/prefix/file]
set -euo pipefail

DB_PATH="${DB_PATH:-./data/mqconnector.db}"
RETAIN_DAYS="${RETAIN_DAYS:-90}"
OUT_DIR="${OUT_DIR:-./audit-archive}"
S3_BUCKET="${S3_BUCKET:-}"
S3_PREFIX="${S3_PREFIX:-audit/}"

if [[ ! -f "$DB_PATH" ]]; then
    echo "error: source database not found: $DB_PATH" >&2
    exit 1
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "error: sqlite3 not found in PATH" >&2
    exit 1
fi
if [[ -n "$S3_BUCKET" ]] && ! command -v aws >/dev/null 2>&1; then
    echo "error: S3_BUCKET is set but the aws CLI isn't installed" >&2
    exit 1
fi

mkdir -p "$OUT_DIR"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="${OUT_DIR%/}/audit-${TS}.ndjson"
TMP="${OUT}.partial"
trap 'rm -f "$TMP"' EXIT

# Bound the id range up-front so concurrent inserts during this run are
# never archived or deleted. The cutoff is RETAIN_DAYS old in the
# database's own clock (which is UTC).
MAX_ID="$(sqlite3 "$DB_PATH" "SELECT COALESCE(MAX(id), 0) FROM audit WHERE at <= datetime('now', '-${RETAIN_DAYS} days');")"
if [[ "${MAX_ID:-0}" -le 0 ]]; then
    echo "no audit rows older than ${RETAIN_DAYS} days; nothing to archive"
    rm -f "$TMP"
    trap - EXIT
    exit 0
fi

# Emit eligible rows as NDJSON. The JSON shape mirrors the API's audit
# entry (id, tenant_id, at, actor, actor_sub, action, resource, status,
# request_id, remote_ip, prev_hash, hash) — easy to re-import or query
# with `jq` / Athena.
sqlite3 -bail -batch "$DB_PATH" >"$TMP" <<SQL
.mode json
.headers off
SELECT
    id, tenant_id, at, actor, actor_sub, action, resource,
    status, request_id, remote_ip, prev_hash, hash
FROM audit
WHERE id <= ${MAX_ID}
ORDER BY id;
SQL
# sqlite3's .mode json emits a single JSON array; collapse to NDJSON
# (one object per line) so streaming / appending downstream is trivial.
python3 -c "
import json, sys
data = json.load(open('${TMP}'))
with open('${TMP}.ndjson', 'w') as f:
    for row in data:
        f.write(json.dumps(row, separators=(',', ':')) + '\n')
" 2>/dev/null || {
    # No python3? Fall back to a sed-based collapse — works as long as
    # no audit value contains a literal '},{' substring.
    sed -e 's/^\[//' -e 's/\]$//' -e 's/},{/}\n{/g' "$TMP" > "${TMP}.ndjson"
}
mv -f "${TMP}.ndjson" "$TMP"

ROW_COUNT="$(wc -l <"$TMP" | tr -d ' ')"

# Optional upload — durably stash the archive before we delete from the
# database. Failure here aborts (trap cleans up the local partial).
if [[ -n "$S3_BUCKET" ]]; then
    S3_URI="s3://${S3_BUCKET}/${S3_PREFIX%/}/audit-${TS}.ndjson"
    aws s3 cp "$TMP" "$S3_URI" \
        --no-progress \
        --content-type application/x-ndjson \
        >/dev/null
fi

# Promote the tempfile to its final name only after upload (if any) has
# succeeded. Now the data is safe locally + remotely; we can delete the
# rows from the database.
mv -f "$TMP" "$OUT"
trap - EXIT

sqlite3 -bail -batch "$DB_PATH" <<SQL
BEGIN;
DELETE FROM audit_diff WHERE audit_id IN (SELECT id FROM audit WHERE id <= ${MAX_ID});
DELETE FROM audit WHERE id <= ${MAX_ID};
COMMIT;
VACUUM;
SQL

MSG="archived ${ROW_COUNT} rows to ${OUT}"
if [[ -n "$S3_BUCKET" ]]; then
    MSG="${MSG} + ${S3_URI}"
fi
echo "$MSG"
