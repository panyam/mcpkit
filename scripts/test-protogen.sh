#!/usr/bin/env bash
# protogen sub-module tests + e2e example. Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/experimental/ext/protogen" && go test ./... -count=1 -timeout 30s
bash "$ROOT/experimental/ext/protogen/scripts/test-e2e.sh"
