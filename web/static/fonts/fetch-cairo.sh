#!/usr/bin/env bash
# Fetch Cairo (SIL OFL 1.1) from the Google Fonts GitHub repo and
# convert to a subsetted woff2 suitable for go:embed.
#
# Run this from a workstation with internet access — NEVER from the
# air-gapped build host. The result file is committed to git so the
# build itself stays sealed.
#
# Requires: curl, python3 with `fonttools` and `brotli` installed:
#   pip3 install fonttools brotli

set -euo pipefail

cd "$(dirname "$0")"

SRC_URL="https://github.com/google/fonts/raw/main/ofl/cairo/Cairo%5Bslnt%2Cwght%5D.ttf"
TTF=".cairo-src.ttf"
OUT="Cairo-VariableFont.woff2"

echo "→ downloading Cairo TTF from google/fonts…"
curl -fsSL "$SRC_URL" -o "$TTF"

echo "→ verifying it looks like a font…"
head -c 4 "$TTF" | xxd | grep -q "0001 0000\|7472 7565\|4f54 544f" || {
  echo "  ERROR: downloaded file doesn't look like a TTF/OTF" >&2
  exit 1
}

if ! command -v python3 >/dev/null; then
  echo "  ERROR: python3 required for woff2 conversion" >&2
  exit 1
fi
if ! python3 -c "import fontTools.ttLib" 2>/dev/null; then
  echo "  ERROR: fonttools required: pip3 install fonttools brotli" >&2
  exit 1
fi

echo "→ converting TTF → woff2…"
python3 - <<PY
from fontTools.ttLib import TTFont
f = TTFont("$TTF")
f.flavor = "woff2"
f.save("$OUT")
print(f"   wrote $OUT")
PY

rm -f "$TTF"
ls -lh "$OUT"
echo "✓ done — commit and rebuild (cd web && npm run build)"
