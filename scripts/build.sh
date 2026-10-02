#!/bin/bash
# Build Farero.app (Universal: arm64 + x86_64) and a zip for GitHub Releases
# (아키텍처 13장).
#
#   scripts/build.sh [version]
#
# Environment:
#   FARERO_GITHUB_CLIENT_ID  GitHub OAuth App client id baked into farerod
#   JOBS                     build parallelism (default 2, keeps the machine usable)
#
# Output: build/Farero.app and build/Farero-<version>.zip
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null | sed 's/^v//')}"
BUILD_NUMBER="$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo 1)"
JOBS="${JOBS:-2}"
OUT="$ROOT/build"
APP="$OUT/Farero.app"

echo "==> farero $VERSION (build $BUILD_NUMBER)"
rm -rf "$APP" "$OUT/go"
mkdir -p "$OUT/go"

# --- Go: farerod (cgo for Keychain) and farero-hook, per arch, then lipo ---
LDFLAGS="-s -w -X main.version=$VERSION -X main.githubClientID=${FARERO_GITHUB_CLIENT_ID:-}"
for arch in arm64 amd64; do
  clang_arch=$([ "$arch" = amd64 ] && echo x86_64 || echo arm64)
  echo "==> go build ($arch)"
  (cd "$ROOT/daemon" && \
    CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $clang_arch" \
    CGO_CFLAGS="-mmacosx-version-min=15.0" CGO_LDFLAGS="-mmacosx-version-min=15.0" \
    go build -p "$JOBS" -trimpath -ldflags "$LDFLAGS" -o "$OUT/go/$arch/" ./cmd/farerod ./cmd/farero-hook)
done
for bin in farerod farero-hook; do
  lipo -create -output "$OUT/go/$bin" "$OUT/go/arm64/$bin" "$OUT/go/amd64/$bin"
done

# --- Swift app, Universal ---
echo "==> swift build (universal)"
swift build --package-path "$ROOT/app" -c release --arch arm64 --arch x86_64 -j "$JOBS" --product Farero
SWIFT_BIN="$(swift build --package-path "$ROOT/app" -c release --arch arm64 --arch x86_64 --show-bin-path)"

# --- Bundle ---
echo "==> assemble $APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Library/LaunchAgents"
cp "$SWIFT_BIN/Farero" "$APP/Contents/MacOS/Farero"
# SwiftPM resource bundles, if any target has resources.
find "$SWIFT_BIN" -maxdepth 1 -name '*.bundle' -exec cp -R {} "$APP/Contents/Resources/" \;
cp "$OUT/go/farerod" "$OUT/go/farero-hook" "$APP/Contents/MacOS/"
sed -e "s/__VERSION__/$VERSION/g" -e "s/__BUILD__/$BUILD_NUMBER/g" \
  "$ROOT/app/Resources/Info.plist" > "$APP/Contents/Info.plist"
cp "$ROOT/app/Resources/LaunchAgents/dev.farero.farerod.plist" "$APP/Contents/Library/LaunchAgents/"
if [ -f "$ROOT/app/Resources/AppIcon.icns" ]; then
  cp "$ROOT/app/Resources/AppIcon.icns" "$APP/Contents/Resources/"
fi
plutil -lint "$APP/Contents/Info.plist" "$APP/Contents/Library/LaunchAgents/dev.farero.farerod.plist" >/dev/null

# --- Ad-hoc signing (MVP is not notarized, Q47) ---
# The Go linker signs only arm64 slices, and lipo output must be re-signed,
# so every executable is signed explicitly, inside out.
echo "==> codesign (ad-hoc)"
for bin in farerod farero-hook; do
  codesign --force --sign - --identifier "dev.farero.$bin" "$APP/Contents/MacOS/$bin"
done
# The app's entitlements allow Apple Events (terminal jump) under the
# hardened runtime; they are harmless without it.
codesign --force --sign - --entitlements "$ROOT/app/Resources/Farero.entitlements" "$APP"
codesign --verify --strict --verbose=1 "$APP"
for bin in farerod farero-hook Farero; do
  lipo -archs "$APP/Contents/MacOS/$bin" | grep -q 'x86_64 arm64' || { echo "$bin is not universal"; exit 1; }
done

ZIP="$OUT/Farero-$VERSION.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"
echo "==> done: $APP"
echo "    $ZIP ($(du -h "$ZIP" | cut -f1))"
