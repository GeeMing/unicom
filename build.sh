#!/usr/bin/env bash
set -euo pipefail

SKIP_TESTS=0
case "${1:-}" in
  "") ;;
  --skip-tests) SKIP_TESTS=1 ;;
  *)
    echo "Usage: ./build.sh [--skip-tests]" >&2
    exit 2
    ;;
esac


VERSION="0.0.1"
GIT_HASH=$(printf '%s@%.7s' "$(git symbolic-ref --short -q HEAD)" "$(git log -1 --format=%H)")
BUILD_TIME=$(date '+%Y-%m-%d_%H:%M:%S_%z')

PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
BUILD_ROOT="$PROJECT_ROOT/build"
STAGE="$BUILD_ROOT/gopath/src/unicom"
GO_EXE="$PROJECT_ROOT/.tools/go1.10.8/go/bin/go.exe"
RESOURCE="$PROJECT_ROOT/resources/unicom_windows_386.syso"
OUTPUT="$BUILD_ROOT/unicom.exe"


if [[ ! -f "$GO_EXE" ]]; then
  echo "Go 1.10.8 was not found: $GO_EXE" >&2
  exit 1
fi
if [[ ! -f "$RESOURCE" ]]; then
  echo "Windows resource was not found: $RESOURCE" >&2
  exit 1
fi

case "$STAGE/" in
  "$BUILD_ROOT/"*) ;;
  *)
    echo "Refusing to clean a path outside the build directory: $STAGE" >&2
    exit 1
    ;;
esac

rm -rf -- "$STAGE"
mkdir -p -- "$STAGE"
cp -- "$PROJECT_ROOT/main.go" "$STAGE/main.go"
cp -- "$PROJECT_ROOT/endpoint_input.go" "$PROJECT_ROOT/endpoint_input_test.go" "$STAGE/"
cp -R -- "$PROJECT_ROOT/internal" "$STAGE/internal"
cp -R -- "$PROJECT_ROOT/vendor" "$STAGE/vendor"
cp -- "$RESOURCE" "$STAGE/unicom_windows_386.syso"

export GOPATH="$(cygpath -w "$BUILD_ROOT/gopath")"
export GOCACHE="$(cygpath -w "$BUILD_ROOT/gocache")"
export GOOS=windows
export GOARCH=386
export CGO_ENABLED=0
export GO111MODULE=off
# All paths passed to the Windows Go binary are already in Windows form.
export MSYS2_ARG_CONV_EXCL='*'

if [[ "$SKIP_TESTS" -eq 0 ]]; then
  "$GO_EXE" test unicom/...
fi

OUTPUT_WIN="$(cygpath -w "$OUTPUT")"
LDFLAGS="-H windowsgui -s -w -X main.VERSION=$VERSION -X main.GIT_HASH=$GIT_HASH -X main.BUILD_TIME=$BUILD_TIME"
"$GO_EXE" build -ldflags "$LDFLAGS" -o "$OUTPUT_WIN" unicom

SIZE="$(stat -c '%s' "$OUTPUT")"
printf 'Build complete: %s (%s bytes)\nVersion: %s | %s | %s\n' "$OUTPUT" "$SIZE" "$VERSION" "$GIT_HASH" "$BUILD_TIME"
