#!/bin/sh
# Build the BlockPanel release: cross-compiled binaries for macOS/Linux x
# amd64/arm64, the all-platform zip, and the assets for a GitHub release.
# Usage: scripts/build-release.sh [output-dir]   (needs Go 1.24+ on PATH or $GO)
#
# Versioning: bump Current in internal/version/version.go, run this script,
# then publish a GitHub release on Dalek70/blockpanel tagged v<version> and
# upload everything from dist/upload/. The panel's built-in updater looks for
# those exact asset names (blockpanel-<os>-<arch>, blockpanel-<version>.zip,
# SHA256SUMS).
set -eu

GO=${GO:-go}
VERSION=$(grep 'const Current' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/')
[ -n "$VERSION" ] || { echo "could not read version from internal/version/version.go"; exit 1; }
OUT=${1:-dist}
NAME="blockpanel-$VERSION"
STAGE="$OUT/$NAME"
UPLOAD="$OUT/upload"

rm -rf "$STAGE" "$UPLOAD"
mkdir -p "$STAGE/bin" "$UPLOAD"

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*}; arch=${target#*/}
  echo "building $os/$arch…"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    "$GO" build -trimpath -ldflags "-s -w" \
    -o "$STAGE/bin/blockpanel-$os-$arch" ./cmd/blockpanel
done

cp scripts/install.sh scripts/uninstall.sh scripts/start.sh scripts/stop.sh "$STAGE/"
chmod +x "$STAGE/install.sh" "$STAGE/uninstall.sh" "$STAGE/start.sh" "$STAGE/stop.sh"
cp README.md "$STAGE/"

(cd "$OUT" && rm -f "$NAME.zip" && zip -qr "$NAME.zip" "$NAME")

# GitHub release assets: zip + standalone binaries + checksums.
cp "$OUT/$NAME.zip" "$UPLOAD/"
cp "$STAGE"/bin/blockpanel-* "$UPLOAD/"
# shasum on macOS, sha256sum on Linux CI runners.
if command -v shasum >/dev/null 2>&1; then
  (cd "$UPLOAD" && shasum -a 256 blockpanel-* > SHA256SUMS)
else
  (cd "$UPLOAD" && sha256sum blockpanel-* > SHA256SUMS)
fi

echo "release zip:    $OUT/$NAME.zip"
echo "github assets:  $UPLOAD/  (upload ALL of these to the v$VERSION release)"
ls -la "$UPLOAD"
