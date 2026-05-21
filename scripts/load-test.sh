#!/usr/bin/env bash
#
# load-test.sh — compare SQLite vs Postgres p99 storage latency under
# the same workload. Wraps `cmd/loadtest` and diffs the JSON output.
#
# Usage:
#
#   # Defaults: 30s × 8 concurrent writers on each backend
#   ./scripts/load-test.sh
#
#   # Override knobs
#   DURATION=60s CONCURRENCY=16 ./scripts/load-test.sh
#
#   # Custom Postgres DSN (default uses localhost:5432 from the postgres:16 image)
#   PG_DSN='postgres://user:pw@db.internal:5432/mqc?sslmode=require' \
#     ./scripts/load-test.sh
#
# Acceptance criterion (from POSTGRES_MIGRATION.md §6): Postgres at
# <= 1.2× SQLite p99 at the same workload is the "go-live" gate for
# multi-tenant deployments. This script prints PASS/FAIL based on
# that ratio.
#
# The script does NOT start the Postgres container — operators run
# their own. The default assumes `docker run -d --rm -p 5432:5432
# -e POSTGRES_PASSWORD=mqc postgres:16` is already up.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DURATION="${DURATION:-30s}"
CONCURRENCY="${CONCURRENCY:-8}"
PG_DSN="${PG_DSN:-postgres://postgres:mqc@localhost:5432/postgres?sslmode=disable}"
# 1.2× ceiling matches the published acceptance criterion. Operators
# tuning for a specific workload can override this — set to e.g.
# 1.5 for read-heavy mixes where the SQLite single-writer is already
# a relative win.
P99_CEILING="${P99_CEILING:-1.2}"

OUT_DIR="$(mktemp -d -t mqc-loadtest-XXXX)"
trap 'rm -rf "$OUT_DIR"' EXIT

echo "running loadtest: duration=$DURATION concurrency=$CONCURRENCY"
echo "  results dir: $OUT_DIR"

# Build once, run twice.
go build -o "$OUT_DIR/loadtest" ./cmd/loadtest

# SQLite first — no external dependency, always runnable.
echo
echo "[1/2] sqlite"
"$OUT_DIR/loadtest" \
  --backend=sqlite \
  --duration="$DURATION" \
  --concurrency="$CONCURRENCY" \
  > "$OUT_DIR/sqlite.json"

# Postgres second. Skip cleanly if the DSN doesn't connect — printing
# a clear hint instead of a stack trace from pgx.
echo
echo "[2/2] postgres"
if ! "$OUT_DIR/loadtest" \
       --backend=postgres \
       --dsn="$PG_DSN" \
       --duration="$DURATION" \
       --concurrency="$CONCURRENCY" \
       > "$OUT_DIR/postgres.json"; then
  cat <<EOF >&2

postgres run failed. Most common cause: no broker reachable at the DSN.
  default DSN: $PG_DSN
  start one with:
    docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=mqc postgres:16
  or override PG_DSN.
EOF
  exit 1
fi

# Diff the p99s using jq if available, otherwise python.
if command -v jq >/dev/null 2>&1; then
  SQL_P99=$(jq -r '.latency_ms.p99' "$OUT_DIR/sqlite.json")
  PG_P99=$(jq -r '.latency_ms.p99' "$OUT_DIR/postgres.json")
  SQL_OPS=$(jq -r '.ops_per_sec' "$OUT_DIR/sqlite.json")
  PG_OPS=$(jq -r '.ops_per_sec' "$OUT_DIR/postgres.json")
elif command -v python3 >/dev/null 2>&1; then
  SQL_P99=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['latency_ms']['p99'])" "$OUT_DIR/sqlite.json")
  PG_P99=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['latency_ms']['p99'])" "$OUT_DIR/postgres.json")
  SQL_OPS=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['ops_per_sec'])" "$OUT_DIR/sqlite.json")
  PG_OPS=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['ops_per_sec'])" "$OUT_DIR/postgres.json")
else
  echo "neither jq nor python3 available; raw JSON dumps below"
  cat "$OUT_DIR/sqlite.json"
  cat "$OUT_DIR/postgres.json"
  exit 0
fi

# Compute ratio and compare. Use awk for portable float math.
RATIO=$(awk -v a="$PG_P99" -v b="$SQL_P99" 'BEGIN { if (b == 0) print 0; else printf "%.2f", a / b }')

cat <<EOF

=== load-test summary ===
  duration       : $DURATION
  concurrency    : $CONCURRENCY

           sqlite     postgres
  ops/sec  $(printf '%-10s %-10s' "$SQL_OPS" "$PG_OPS")
  p99 (ms) $(printf '%-10s %-10s' "$SQL_P99" "$PG_P99")

  postgres/sqlite p99 ratio: $RATIO  (acceptance <= $P99_CEILING)
EOF

# PASS/FAIL gate. POSTGRES_MIGRATION.md publishes 1.2× as the
# acceptance threshold; an operator overriding P99_CEILING for a
# specific workload is responsible for that decision.
PASS=$(awk -v r="$RATIO" -v c="$P99_CEILING" 'BEGIN { print (r <= c) ? 1 : 0 }')
if [ "$PASS" = "1" ]; then
  echo "  result: PASS"
else
  echo "  result: FAIL — postgres p99 exceeds ${P99_CEILING}× sqlite p99"
  exit 2
fi
