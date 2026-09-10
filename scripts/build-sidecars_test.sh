#!/usr/bin/env bash
# Verifies that the sidecar build produces exactly the six binaries Tauri
# expects, correctly named, non-empty, executable, and compiled for the right
# platform (GOOS/GOARCH).
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

# Helper function to get expected GOOS and GOARCH for a binary name
get_expected_target() {
  local name="$1"
  case "$name" in
    engine-x86_64-apple-darwin)
      echo "darwin amd64"
      ;;
    engine-aarch64-apple-darwin)
      echo "darwin arm64"
      ;;
    engine-x86_64-unknown-linux-gnu)
      echo "linux amd64"
      ;;
    engine-aarch64-unknown-linux-gnu)
      echo "linux arm64"
      ;;
    engine-x86_64-pc-windows-msvc.exe)
      echo "windows amd64"
      ;;
    engine-aarch64-pc-windows-msvc.exe)
      echo "windows arm64"
      ;;
    *)
      echo "unknown unknown"
      ;;
  esac
}

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

  # Verify the binary was compiled for the correct platform
  expected_goos_goarch=$(get_expected_target "$name")
  read -r expected_goos expected_goarch <<<"$expected_goos_goarch"

  # Extract GOOS and GOARCH from the binary using go version -m
  actual_goos=$(go version -m "$path" | grep "build.*GOOS=" | awk '{print $NF}' | sed 's/GOOS=//')
  actual_goarch=$(go version -m "$path" | grep "build.*GOARCH=" | awk '{print $NF}' | sed 's/GOARCH=//')

  if [[ "$actual_goos" != "$expected_goos" ]] || [[ "$actual_goarch" != "$expected_goarch" ]]; then
    echo "FAIL: $path: expected GOOS=$expected_goos GOARCH=$expected_goarch, got GOOS=$actual_goos GOARCH=$actual_goarch"
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
