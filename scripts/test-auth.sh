#!/usr/bin/env bash
# ext/auth sub-module tests. Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/ext/auth" && go test ./... -count=1 -timeout 30s
