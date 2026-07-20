#!/usr/bin/env bash
# ext/ui: Go + JS bridge unit tests. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"
go test ./... -count=1 -timeout 30s
cd assets && pnpm install --frozen-lockfile --silent && pnpm test
