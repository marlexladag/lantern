#!/usr/bin/env bash
# Cross-compiles the engine sidecar for every target Tauri bundles.
#
# Tauri resolves an externalBin entry "binaries/engine" by appending the host's
# Rust target triple, so the file names below are a hard contract with
# src-tauri/tauri.conf.json — do not rename them independently.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT_DIR="src-tauri/binaries"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

# goos goarch rust-triple suffix
TARGETS=(
  "darwin  amd64 x86_64-apple-darwin         "
  "darwin  arm64 aarch64-apple-darwin        "
  "linux   amd64 x86_64-unknown-linux-gnu    "
  "linux   arm64 aarch64-unknown-linux-gnu   "
  "windows amd64 x86_64-pc-windows-msvc  .exe"
  "windows arm64 aarch64-pc-windows-msvc .exe"
)

mkdir -p "$OUT_DIR"

for target in "${TARGETS[@]}"; do
  read -r goos goarch triple suffix <<<"$target"
  suffix="${suffix:-}"
  out="${OUT_DIR}/engine-${triple}${suffix}"

  echo "building ${goos}/${goarch} -> ${out}"
  # CGO stays off so these cross-compile from any host with no toolchain setup.
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/engine
  chmod +x "$out"
done

echo "built ${#TARGETS[@]} sidecar binaries into ${OUT_DIR}"
