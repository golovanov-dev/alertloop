#!/usr/bin/env bash
# Cross-compile AlertLoop release binaries. modernc SQLite is pure Go, so builds
# are CGO-free and cross-compile without a C toolchain.
#
# Usage: VERSION=v0.1.0 ./scripts/build-release.sh
set -euo pipefail

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT_DIR="${OUT_DIR:-dist}"
PKG="./cmd/alertloop"

# linux/{amd64,arm64} are the required release targets; darwin/windows are
# provided for local evaluation.
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
)

mkdir -p "$OUT_DIR"
echo "Building AlertLoop $VERSION -> $OUT_DIR"

for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  ext=""
  [[ "$GOOS" == "windows" ]] && ext=".exe"
  name="alertloop_${VERSION}_${GOOS}_${GOARCH}${ext}"

  echo "  - $GOOS/$GOARCH"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build \
    -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$OUT_DIR/$name" "$PKG"

  # Package the binary. Windows gets a .zip when `zip` is available (nicer for
  # Windows users), otherwise a .tar.gz so the build never fails on a machine
  # without zip.
  if [[ "$GOOS" == "windows" ]] && command -v zip >/dev/null 2>&1; then
    (cd "$OUT_DIR" && zip -q "alertloop_${VERSION}_${GOOS}_${GOARCH}.zip" "$name")
  else
    tar -C "$OUT_DIR" -czf "$OUT_DIR/alertloop_${VERSION}_${GOOS}_${GOARCH}.tar.gz" "$name"
  fi
done

echo "Generating checksums"
(cd "$OUT_DIR" && sha256sum alertloop_* > "checksums_${VERSION}.txt" 2>/dev/null || shasum -a 256 alertloop_* > "checksums_${VERSION}.txt")

echo "Done. Artifacts in $OUT_DIR/"
