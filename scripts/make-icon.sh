#!/usr/bin/env bash
# Regenerates app/Resources/AppIcon.icns from the LighthouseKit character.
# The .icns is committed, so CI does not need to render it.
# Optional: PREVIEW_DIR=<dir> also writes preview PNGs (1024, 128, 32, 16).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
ICONSET="$TMP/AppIcon.iconset"
swift run --package-path "$ROOT/app/LighthouseKit" -c release lighthouse-icon "$ICONSET" ${PREVIEW_DIR:+"$PREVIEW_DIR"}
mkdir -p "$ROOT/app/Resources"
iconutil -c icns "$ICONSET" -o "$ROOT/app/Resources/AppIcon.icns"
echo "wrote $ROOT/app/Resources/AppIcon.icns"
