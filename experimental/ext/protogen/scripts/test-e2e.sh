#!/usr/bin/env bash
# protogen: install plugin, regenerate examples, compile + run e2e tests. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$DIR/scripts/install.sh"
echo "==> e2e: bookservice example"
cd "$DIR/../../../examples/protogen/bookservice" && rm -rf gen && buf generate && go test -count=1 -timeout 30s .
echo "==> e2e: passed"
