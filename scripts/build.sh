#!/usr/bin/env bash
# Build the mqconnector binary. By default produces a CGO-free build without
# IBM MQ support. Pass --ibmmq to include the IBM MQ client (requires CGO + the
# IBM MQ Redistributable Client in ibmmq_dist/).
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="$(cat VERSION 2>/dev/null || echo dev)"
TAGS=""
CGO=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --ibmmq) TAGS="ibmmq"; CGO=1 ;;
    --version) shift; VERSION="$1" ;;
    -h|--help)
      echo "Usage: $0 [--ibmmq] [--version X.Y.Z]"
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

OUT_DIR="dist"
OUT_BIN="$OUT_DIR/mqconnector"
mkdir -p "$OUT_DIR"

# 1) Build the SvelteKit frontend into internal/web/dist if Node is available
#    and the web directory exists.
if [[ -d web ]] && command -v npm >/dev/null 2>&1; then
  echo "▸ Building frontend"
  (
    cd web
    if [[ ! -d node_modules ]]; then npm ci --no-audit --no-fund; fi
    npm run build
  )
fi

# 2) Compile the Go binary.
echo "▸ Building binary (tags='${TAGS}', CGO_ENABLED=${CGO}, version=${VERSION})"

EXTRA_BUILD_FLAGS=()
if [[ -n "$TAGS" ]]; then
  EXTRA_BUILD_FLAGS+=("-tags" "$TAGS")
fi

# Provide CGO flags only when building with the ibmmq tag — otherwise we want a
# pure-Go build with CGO off.
if [[ "$CGO" == "1" ]]; then
  IBMMQ_INC="${IBMMQ_INC:-$PWD/ibmmq_dist/inc}"
  IBMMQ_LIB="${IBMMQ_LIB:-$PWD/ibmmq_dist/lib64}"
  export CGO_CFLAGS="-I$IBMMQ_INC"
  export CGO_LDFLAGS="-L$IBMMQ_LIB -Wl,-rpath,$IBMMQ_LIB"
fi

CGO_ENABLED=$CGO go build \
  ${EXTRA_BUILD_FLAGS[@]+"${EXTRA_BUILD_FLAGS[@]}"} \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$OUT_BIN" \
  ./cmd/mqconnector

echo "✓ Built $OUT_BIN ($(du -h "$OUT_BIN" | cut -f1))"
