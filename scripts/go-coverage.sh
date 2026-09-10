#!/usr/bin/env bash
# Computes total Go statement coverage across every package, including
# cmd/engine's main() — which `go test`'s own instrumentation cannot see on
# its own, because cmd/engine/main_test.go deliberately drives the engine as
# a real subprocess (real pipes, real signals), never calling main() itself
# in-process. `go test -coverprofile` therefore always reports 0% for that
# package regardless of how well it's tested; that is a measurement gap, not
# a testing one.
#
# Fixed the same way the Go toolchain fixes it for any instrumented binary:
# main_test.go builds the subprocess with `go build -cover` and points its
# GOCOVERDIR at the directory this script exports below, so the subprocess
# writes real counter data on exit. `go tool covdata` then converts that
# data to the same legacy profile format `-coverprofile` produces, and it is
# concatenated with the normal in-process profile from internal/... — the
# two are disjoint (this script never asks `go test` to instrument
# cmd/engine directly), so concatenation is a plain union, not a counter
# merge.
#
# Usage: ./scripts/go-coverage.sh [--check]
#   (no args)  print the per-function and total coverage report.
#   --check    also fail (non-zero exit) if total statement coverage is
#              below THRESHOLD. This is the CI gate.
set -euo pipefail

cd "$(dirname "$0")/.."

THRESHOLD=100.0

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

COVDIR="$WORKDIR/covdata"
mkdir -p "$COVDIR"

# internal/health and internal/rpc are covered the ordinary way: go test
# calls their code in-process and instruments it directly.
go test ./internal/... -coverprofile="$WORKDIR/internal.out" >/dev/null

# cmd/engine's tests build and exec the engine binary as a subprocess, built
# with -cover (see buildEngine in main_test.go). Exporting GOCOVERDIR here
# reaches that subprocess's environment: engineEnv() in main_test.go
# forwards whatever GOCOVERDIR it finds in its own environment (go test's
# own -coverprofile machinery does not propagate GOCOVERDIR to child
# processes on its own, so this has to be explicit).
GOCOVERDIR="$COVDIR" go test ./cmd/engine/... >/dev/null

if [[ -z "$(ls -A "$COVDIR" 2>/dev/null)" ]]; then
  echo "FAIL: no coverage data was written to $COVDIR by cmd/engine's subprocess tests" >&2
  echo "(every cmd/engine test that shuts the engine down cleanly should write counter data there)" >&2
  exit 1
fi

# Convert the raw counter data into the same legacy profile format
# -coverprofile produces, so it can be combined with internal.out.
go tool covdata textfmt -i="$COVDIR" -o="$WORKDIR/main.out"

# The two profiles cover entirely disjoint packages (internal/* vs
# cmd/engine), so this is a plain concatenation: one "mode:" header, then
# both bodies.
{
  head -n1 "$WORKDIR/internal.out"
  tail -n +2 "$WORKDIR/internal.out"
  tail -n +2 "$WORKDIR/main.out"
} > "$WORKDIR/combined.out"

FUNC_REPORT="$(go tool cover -func="$WORKDIR/combined.out")"
echo "$FUNC_REPORT"

TOTAL="$(echo "$FUNC_REPORT" | awk '/^total:/ {gsub("%","",$NF); print $NF}')"

echo
echo "Total statement coverage: ${TOTAL}%"

if [[ "${1:-}" == "--check" ]]; then
  # Bash has no float comparison; awk does the threshold check.
  if awk -v t="$TOTAL" -v th="$THRESHOLD" 'BEGIN { exit !(t >= th) }'; then
    echo "OK: coverage meets the ${THRESHOLD}% threshold"
  else
    echo "FAIL: coverage ${TOTAL}% is below the ${THRESHOLD}% threshold" >&2
    exit 1
  fi
fi
