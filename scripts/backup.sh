#!/usr/bin/env bash
# Snapshot the mqConnector SQLite store into a single restorable archive.
#
# What's captured:
#   - The SQLite database file via the `.backup` API (consistent online
#     snapshot — safe to run while the bridge is processing messages).
#   - The auth file path if present (legacy; kept for backwards-compat
#     with deployments that still embed SimpleAuth state).
#   - The TLS certificate + key referenced in the live config (so a
#     restore on a new host doesn't need a manual cert dance).
#   - The config.yaml itself.
#
# Usage:
#   scripts/backup.sh [output-tarball]
#
# Defaults to backups/mqconnector-<timestamp>.tar.gz.
set -euo pipefail

cd "$(dirname "$0")/.."

# Resolve config — env override wins, else config.yaml, else config.example.yaml.
CONFIG_PATH="${MQC_CONFIG:-config.yaml}"
[[ -f "$CONFIG_PATH" ]] || CONFIG_PATH="config.example.yaml"

# Pull just the bits we need from the config (single-pass grep — no yq
# dependency).
# BSD sed (macOS) doesn't grok \s; [[:space:]] is the portable spelling.
DSN=$(grep -E '^[[:space:]]+dsn:' "$CONFIG_PATH" 2>/dev/null | head -1 \
  | sed -E 's/.*dsn:[[:space:]]*"([^"]+)".*/\1/' || true)
CERT_FILE=$(grep -E '^[[:space:]]+cert_file:' "$CONFIG_PATH" 2>/dev/null | head -1 \
  | sed -E 's/.*cert_file:[[:space:]]*"?([^"]+)"?.*/\1/' || true)
KEY_FILE=$(grep -E '^[[:space:]]+key_file:' "$CONFIG_PATH" 2>/dev/null | head -1 \
  | sed -E 's/.*key_file:[[:space:]]*"?([^"]+)"?.*/\1/' || true)

# Extract just the path from a SQLite DSN like "file:./data/x.db?...".
DB_PATH="${DSN#file:}"
DB_PATH="${DB_PATH%%\?*}"

if [[ -z "$DB_PATH" || ! -f "$DB_PATH" ]]; then
  echo "✗ database not found at '${DB_PATH:-<unset>}' (from $CONFIG_PATH)" >&2
  exit 1
fi

TS=$(date -u +%Y%m%d-%H%M%S)
OUT="${1:-backups/mqconnector-${TS}.tar.gz}"
mkdir -p "$(dirname "$OUT")"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

echo "▸ Snapshotting $DB_PATH via .backup"
# .backup creates a transactionally-consistent file copy without blocking
# writers. Works regardless of WAL/journal mode.
if command -v sqlite3 >/dev/null 2>&1; then
  sqlite3 "$DB_PATH" ".backup '$STAGE/mqconnector.db'"
else
  echo "✗ sqlite3 CLI not installed — falling back to raw file copy" >&2
  echo "  (acceptable only when the bridge is stopped)" >&2
  cp "$DB_PATH" "$STAGE/mqconnector.db"
fi

cp "$CONFIG_PATH" "$STAGE/config.yaml" 2>/dev/null || true
[[ -f "$CERT_FILE" ]] && cp "$CERT_FILE" "$STAGE/$(basename "$CERT_FILE")"
[[ -f "$KEY_FILE"  ]] && cp "$KEY_FILE"  "$STAGE/$(basename "$KEY_FILE")"

# Capture the mqconnector binary's version too, so a restorer knows
# which release the schema corresponds to.
[[ -f VERSION ]] && cp VERSION "$STAGE/VERSION"

tar -C "$STAGE" -czf "$OUT" .
echo "✓ wrote $OUT ($(du -h "$OUT" | cut -f1))"
