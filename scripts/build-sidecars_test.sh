#!/usr/bin/env bash
# Verifies that the sidecar build produces exactly the six binaries Tauri
# expects, correctly named, non-empty, and executable.
set -euo pipefail

cd "$(dirname "$0")/.."
OUT_DIR="src-tauri/binaries"

rm -rf "$OUT_DIR"
./scripts/build-sidecars.sh

expected=(
  "engine-x86_64-apple-darwin"
  "engine-aarch64-apple-darwin"
  "engine-x86_64-unknown-linux-gnu"
  "engine-aarch64-unknown-linux-gnu"
  "engine-x86_64-pc-windows-msvc.exe"
  "engine-aarch64-pc-windows-msvc.exe"
)

fail=0
for name in "${expected[@]}"; do
  path="$OUT_DIR/$name"
  if [[ ! -f "$path" ]]; then
    echo "FAIL: missing $path"
    fail=1
    continue
  fi
  if [[ ! -s "$path" ]]; then
    echo "FAIL: $path is empty"
    fail=1
  fi
  if [[ ! -x "$path" ]]; then
    echo "FAIL: $path is not executable"
    fail=1
  fi
done

actual_count=$(find "$OUT_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ')
if [[ "$actual_count" != "6" ]]; then
  echo "FAIL: expected 6 binaries, found $actual_count"
  fail=1
fi

if [[ "$fail" != "0" ]]; then
  echo "sidecar build test FAILED"
  exit 1
fi
echo "sidecar build test PASSED: 6/6 targets"
