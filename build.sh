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

VERSION=$(git describe --tags --exact-match 2>/dev/null || true)
GIT_HASH=$(printf '%s@%.7s' "$(git symbolic-ref --short -q HEAD 2>/dev/null || git rev-parse --short HEAD 2>/dev/null)" "$(git log -1 --format=%H 2>/dev/null)")
BUILD_TIME=$(date '+%Y-%m-%d_%H:%M:%S')

PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
BUILD_ROOT="$PROJECT_ROOT/build"
STAGE="$BUILD_ROOT/gopath/src/unicom"
# Check and install go1.10.8 if needed
if ! command -v go1.10.8 >/dev/null 2>&1; then
  if command -v go >/dev/null 2>&1; then
    SYSTEM_GOBIN="$(go env GOBIN 2>/dev/null || true)"
    SYSTEM_GOPATH="$(go env GOPATH 2>/dev/null || true)"
    if [[ -n "$SYSTEM_GOBIN" && -d "$SYSTEM_GOBIN" ]]; then
      export PATH="$SYSTEM_GOBIN:$PATH"
    fi
    if [[ -n "$SYSTEM_GOPATH" ]]; then
      FIRST_GOPATH="$(echo "$SYSTEM_GOPATH" | cut -d';' -f1)"
      if command -v cygpath >/dev/null 2>&1; then
        FIRST_GOPATH="$(cygpath -u "$FIRST_GOPATH" 2>/dev/null || echo "$FIRST_GOPATH")"
      fi
      if [[ -d "$FIRST_GOPATH/bin" ]]; then
        export PATH="$FIRST_GOPATH/bin:$PATH"
      fi
    fi
  fi
fi

if ! command -v go1.10.8 >/dev/null 2>&1; then
  echo "go1.10.8 not found. Installing via 'go install golang.org/dl/go1.10.8@latest'..."
  if ! command -v go >/dev/null 2>&1; then
    echo "Error: 'go' is required to install go1.10.8, but was not found in PATH." >&2
    exit 1
  fi
  go install golang.org/dl/go1.10.8@latest

  SYSTEM_GOBIN="$(go env GOBIN 2>/dev/null || true)"
  SYSTEM_GOPATH="$(go env GOPATH 2>/dev/null || true)"
  if [[ -n "$SYSTEM_GOBIN" && -d "$SYSTEM_GOBIN" ]]; then
    export PATH="$SYSTEM_GOBIN:$PATH"
  fi
  if [[ -n "$SYSTEM_GOPATH" ]]; then
    FIRST_GOPATH="$(echo "$SYSTEM_GOPATH" | cut -d';' -f1)"
    if command -v cygpath >/dev/null 2>&1; then
      FIRST_GOPATH="$(cygpath -u "$FIRST_GOPATH" 2>/dev/null || echo "$FIRST_GOPATH")"
    fi
    if [[ -d "$FIRST_GOPATH/bin" ]]; then
      export PATH="$FIRST_GOPATH/bin:$PATH"
    fi
  fi
fi

if ! command -v go1.10.8 >/dev/null 2>&1; then
  echo "Error: Failed to find or install go1.10.8." >&2
  exit 1
fi

go1.10.8 download

GO_EXE="go1.10.8"
RESOURCE="$PROJECT_ROOT/resources/unicom_windows_386.syso"
OUTPUT="$BUILD_ROOT/unicom.exe"

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
if [[ -n "$VERSION" ]]; then
  DISPLAY_VERSION="$VERSION | $GIT_HASH | $BUILD_TIME"
else
  DISPLAY_VERSION="$GIT_HASH | $BUILD_TIME"
fi
printf 'Build complete: %s (%s bytes)\nVersion: %s\n' "$OUTPUT" "$SIZE" "$DISPLAY_VERSION"
