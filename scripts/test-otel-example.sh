#!/usr/bin/env bash
# examples/otel/stdout smoke test. Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/examples/otel/stdout" && go test ./... -count=1 -timeout 30s
