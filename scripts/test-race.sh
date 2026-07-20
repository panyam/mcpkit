#!/usr/bin/env bash
# Root module: unit tests with the race detector. Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
go test -race ./... -count=1 -timeout 60s
