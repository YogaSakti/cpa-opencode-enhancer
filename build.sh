#!/usr/bin/env bash
# Build the opencode-enhancer CLIProxyAPI plugin.
#
# The CLIProxyAPI runtime image is Debian-based (glibc). Building with an
# alpine/musl Go image produces a musl-linked .so that fails to dlopen at
# runtime ("libc.musl-x86_64.so.1: cannot open shared object file"), so this
# script requires CGO and a glibc toolchain.
set -euo pipefail

cd "$(dirname "$0")"

PLUGIN_ID="opencode-enhancer"
OUT_DIR="${OUT_DIR:-dist}"
GOOS_TARGET="${GOOS:-linux}"
GOARCH_TARGET="${GOARCH:-amd64}"

case "$GOOS_TARGET" in
  linux|freebsd) EXT="so" ;;
  darwin) EXT="dylib" ;;
  windows) EXT="dll" ;;
  *) echo "unsupported GOOS: $GOOS_TARGET" >&2; exit 1 ;;
esac

DEST_DIR="$OUT_DIR/$GOOS_TARGET/$GOARCH_TARGET"
mkdir -p "$DEST_DIR"

echo ">> go vet"
go vet ./...

echo ">> go test"
go test ./...

echo ">> build $GOOS_TARGET/$GOARCH_TARGET -> $DEST_DIR/$PLUGIN_ID.$EXT"
CGO_ENABLED=1 GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" \
  go build -buildmode=c-shared -trimpath \
  -ldflags "-s -w" \
  -o "$DEST_DIR/$PLUGIN_ID.$EXT" .

echo ">> done"
ls -lh "$DEST_DIR/$PLUGIN_ID.$EXT"
file "$DEST_DIR/$PLUGIN_ID.$EXT" || true
