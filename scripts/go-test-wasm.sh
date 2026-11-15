#!/usr/bin/env bash
# Runs `go test` for GOOS=js GOARCH=wasm under node, in the given module
# directory. Extra arguments go to `go test`.
#
#   scripts/go-test-wasm.sh examples/getting-started ./server
#
# Go's node runner passes the environment into the wasm instance and fails
# with "total length of command line and environment variables exceeds limit"
# on a large one, so the tests run with a minimal environment (#1711).
set -euo pipefail
dir="$1"; shift
command -v node >/dev/null || { echo "go-test-wasm: node is required" >&2; exit 1; }
goroot="$(go env GOROOT)"
cd "$dir"
exec env -i \
  HOME="$HOME" \
  PATH="$goroot/lib/wasm:$goroot/bin:$(dirname "$(command -v node)"):/usr/bin:/bin" \
  GOCACHE="$(go env GOCACHE)" \
  GOMODCACHE="$(go env GOMODCACHE)" \
  GOPATH="$(go env GOPATH)" \
  GOTOOLCHAIN="$(go env GOTOOLCHAIN)" \
  GOOS=js GOARCH=wasm \
  go test -count=1 "$@"
