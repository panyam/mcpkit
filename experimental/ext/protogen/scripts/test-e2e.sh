#!/usr/bin/env bash
# protogen: install plugin, regenerate examples, compile + run e2e tests. Runner-agnostic.
#
# One command per line: bash's set -e ignores a failure partway through an
# && chain, so `generate && go test` used to print "passed" after a failed
# generate (#1551).
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXAMPLE="${PROTOGEN_E2E_EXAMPLE:-$DIR/../../../examples/protogen/bookservice}"
bash "$DIR/scripts/install.sh"
echo "==> e2e: bookservice example"
bash "$DIR/scripts/generate.sh" "$EXAMPLE"
cd "$EXAMPLE"
go test -count=1 -timeout 30s .
echo "==> e2e: passed"
