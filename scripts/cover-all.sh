#!/usr/bin/env bash
# HTML coverage for the root module plus the sub-modules with meaningful test
# suites, into REPORT_DIR.
#
# Sub-module failures are tolerated, as they were in the recipe this replaces: a
# module whose tests fail should not cost you the reports for the ones that
# passed. The root module is not tolerated — if that fails you want to know.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$PWD"

REPORT_DIR="${REPORT_DIR:-tests/reports}"
COVER_MODS="${COVER_MODS:-ext/auth ext/ui}"

mkdir -p "$REPORT_DIR"

echo "==> coverage: root module"
go test -coverprofile="$REPORT_DIR/coverage-root.out" ./... -count=1 -timeout 30s
go tool cover -html="$REPORT_DIR/coverage-root.out" -o "$REPORT_DIR/coverage-root.html"

for mod in $COVER_MODS; do
    echo "==> coverage: $mod"
    slug="$(echo "$mod" | tr / -)"
    # Absolute, so the profile lands in REPORT_DIR whatever the module's depth.
    # The recipe hardcoded ../../ and only worked for two-level modules.
    (cd "$mod" && go test -coverprofile="$ROOT/$REPORT_DIR/coverage-$slug.out" ./... -count=1 -timeout 30s) || true
    go tool cover -html="$REPORT_DIR/coverage-$slug.out" -o "$REPORT_DIR/coverage-$slug.html" 2>/dev/null || true
done

echo ""
echo "Coverage reports:"
ls -1 "$REPORT_DIR"/coverage-*.html 2>/dev/null
