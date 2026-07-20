#!/usr/bin/env bash
# Root module: run tests with coverage + generate the HTML report.
# Runner-agnostic. REPORT_DIR env overrides the output dir (default tests/reports).
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPORT_DIR="${REPORT_DIR:-tests/reports}"
cd "$ROOT"
mkdir -p "$REPORT_DIR"
go test -coverprofile="$REPORT_DIR/coverage.out" ./... -count=1 -timeout 120s
go tool cover -html="$REPORT_DIR/coverage.out" -o "$REPORT_DIR/coverage.html"
echo "Coverage report: $REPORT_DIR/coverage.html"
