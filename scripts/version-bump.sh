#!/usr/bin/env bash
# Bump the version in VERSION. Use:
#   scripts/version-bump.sh patch    # 1.0.0 -> 1.0.1
#   scripts/version-bump.sh minor    # 1.0.0 -> 1.1.0
#   scripts/version-bump.sh major    # 1.0.0 -> 2.0.0
#   scripts/version-bump.sh 1.2.3    # set explicitly
#
# Per department workflow, the agent must confirm with the user before invoking
# this. This script does not commit anything.
set -euo pipefail

cd "$(dirname "$0")/.."

current="$(cat VERSION 2>/dev/null || echo 0.0.0)"
IFS=. read -r major minor patch <<<"$current"

case "${1:-}" in
  major)   major=$((major + 1)); minor=0; patch=0 ;;
  minor)   minor=$((minor + 1)); patch=0 ;;
  patch)   patch=$((patch + 1)) ;;
  [0-9]*)  next="$1" ;;
  *)
    echo "Usage: $0 (major|minor|patch|X.Y.Z)" >&2
    exit 2
    ;;
esac

next="${next:-${major}.${minor}.${patch}}"
echo "$next" > VERSION
echo "VERSION: $current → $next"
