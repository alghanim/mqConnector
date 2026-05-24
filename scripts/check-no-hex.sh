#!/usr/bin/env bash
# Fails if any file under web/src contains a raw hex color (#RGB, #RGBA,
# #RRGGBB, or #RRGGBBAA) anywhere that ships to the browser as live style.
# All colors must come from CSS custom properties defined in
# web/src/lib/brand-tokens.css.
#
# Why this exists:
#   The department branding guide locks color to a token system. A raw hex
#   anywhere in a component is a regression — even if it happens to match a
#   token today, the next palette rev breaks it silently. This script is the
#   cheap pre-commit / CI tripwire that catches it before review.
#
# What this DOES scan:
#   web/src/**/*.{svelte,css,scss,tsx,ts,jsx,js,html}
#
# What this DOES NOT count as a violation:
#   - web/src/lib/brand-tokens.css      (canonical palette file)
#   - *.test.* and *.spec.*             (test fixtures may use literal hex)
#   - hex inside /* ... */ block comments  (CSS / JS / TS / Svelte)
#   - hex inside <!-- ... --> comments  (HTML / Svelte template)
#   - hex inside // line comments       (JS / TS / Svelte script)
#   - any line carrying the marker      check-no-hex: ignore
#     (escape hatch for unavoidable inline cases like <meta name="theme-color">
#      which the browser reads before any stylesheet loads)
#
# Exit codes:
#   0 — clean
#   1 — at least one raw hex match was found in shipping code
#
# Uses git ls-files so .gitignore is respected (no scanning .svelte-kit /
# build / node_modules). Bash 3.2 compatible (macOS default).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WEB_SRC="${REPO_ROOT}/web/src"

if [ ! -d "$WEB_SRC" ]; then
  echo "check-no-hex: web/src not found at $WEB_SRC — nothing to scan" >&2
  exit 0
fi

cd "$REPO_ROOT"

TMP_FILES="$(mktemp -t check_no_hex_files.XXXXXX)"
TMP_STRIPPED="$(mktemp -t check_no_hex_strip.XXXXXX)"
TMP_HITS="$(mktemp -t check_no_hex_hits.XXXXXX)"
trap 'rm -f "$TMP_FILES" "$TMP_STRIPPED" "$TMP_HITS"' EXIT

git ls-files "$WEB_SRC" \
  | grep -E '\.(svelte|css|scss|tsx|ts|jsx|js|html)$' \
  | grep -v 'brand-tokens\.css' \
  | grep -v '\.test\.' \
  | grep -v '\.spec\.' \
  > "$TMP_FILES" || true

FILE_COUNT=$(wc -l < "$TMP_FILES" | tr -d ' ')
if [ "$FILE_COUNT" -eq 0 ]; then
  echo "check-no-hex: no scannable files under $WEB_SRC"
  exit 0
fi

# Hex-color regex tuned for false-positive avoidance.
#
# Match a literal '#' followed by EXACTLY 3, 4, 6, or 8 hex digits and then
# a non-word boundary. Anchoring to the four valid CSS color lengths rejects
# URL fragments (#section-id), SVG id refs (url(#grad)), and Markdown anchors —
# none of which both contain only hex digits AND happen to be exactly the right
# length.
HEX_PATTERN='#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b'

# Comment-stripping preprocessor. Replaces comment characters with spaces so
# line numbers stay aligned with the original file, then blanks any line that
# carries the `check-no-hex: ignore` marker.
strip_comments() {
  perl -0777 -pe '
    # Honor the escape hatch FIRST so a marker carried inside an HTML or
    # block comment still neutralizes its own line before the comment text
    # itself is stripped away.
    s{^.*check-no-hex:\s*ignore.*$}{}gm;
    # Block comments /* ... */ — replace with spaces (preserve newlines).
    s{/\*.*?\*/}{ my $m=$&; $m =~ s/[^\n]/ /g; $m }ges;
    # HTML comments <!-- ... --> — same trick.
    s{<!--.*?-->}{ my $m=$&; $m =~ s/[^\n]/ /g; $m }ges;
    # Line comments // ... to end-of-line. Naive but good enough — false
    # positives only happen if a hex sits inside a string that also contains
    # //, which is vanishingly rare. Skip lines that look like URLs (://).
    s{(^|[^:])//[^\n]*}{$1}gm;
  ' "$1"
}

VIOLATIONS=0
while IFS= read -r file; do
  [ -z "$file" ] && continue

  strip_comments "$file" > "$TMP_STRIPPED"

  if grep -nE "$HEX_PATTERN" "$TMP_STRIPPED" 2>/dev/null > "$TMP_HITS"; then
    if [ -s "$TMP_HITS" ]; then
      echo "$file:" >&2
      cat "$TMP_HITS" >&2
      HITS=$(wc -l < "$TMP_HITS" | tr -d ' ')
      VIOLATIONS=$((VIOLATIONS + HITS))
    fi
  fi
done < "$TMP_FILES"

if [ "$VIOLATIONS" -gt 0 ]; then
  echo "" >&2
  echo "check-no-hex: $VIOLATIONS raw hex color(s) found outside brand-tokens.css" >&2
  echo "Use CSS custom properties from web/src/lib/brand-tokens.css instead." >&2
  echo "If a literal hex is genuinely unavoidable (e.g. <meta name=\"theme-color\">)," >&2
  echo "add the marker 'check-no-hex: ignore' to that line." >&2
  exit 1
fi

echo "check-no-hex: OK (scanned $FILE_COUNT files; no raw hex outside brand-tokens.css)"
exit 0
