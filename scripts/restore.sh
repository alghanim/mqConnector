#!/usr/bin/env bash
# Restore an mqConnector snapshot produced by scripts/backup.sh.
#
# Usage:
#   scripts/restore.sh <tarball> [target-dir]
#
# The target dir defaults to ./restore-<timestamp>. After unpacking,
# review the contents, then point your live config at the restored
# files (or copy them into place if you're restoring in-situ).
#
# This script is intentionally conservative — it never touches a running
# mqConnector's data dir directly. Stop the service, restore, then start.
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <tarball> [target-dir]" >&2
  exit 2
fi

TARBALL="$1"
TARGET="${2:-restore-$(date -u +%Y%m%d-%H%M%S)}"

if [[ ! -f "$TARBALL" ]]; then
  echo "✗ archive not found: $TARBALL" >&2
  exit 1
fi

mkdir -p "$TARGET"
tar -C "$TARGET" -xzf "$TARBALL"

echo "✓ restored to $TARGET"
echo
echo "Inventory:"
ls -la "$TARGET"
echo
if [[ -f "$TARGET/VERSION" ]]; then
  echo "Snapshot was taken against mqconnector v$(cat "$TARGET/VERSION")"
fi
echo
cat <<'EOF'
Next steps:
  1. Stop the mqconnector service if it is running on this host.
  2. Replace the live database file with the one in the restored dir,
     OR point your config's storage.dsn at the restored path.
  3. Replace the TLS cert / key files if the snapshot's hostname
     matches; otherwise re-issue new certs for the new host.
  4. Start mqconnector. Migrations are idempotent — a fresher binary
     will upgrade the schema on first boot without losing data.
EOF
