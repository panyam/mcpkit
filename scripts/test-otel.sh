#!/usr/bin/env bash
# SEP-414 ext/otel adapter sub-module tests. Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/ext/otel" && go test ./... -count=1 -timeout 30s
