#!/usr/bin/env bash
# Selftest for test-e2e.sh (#1551). Points it at a copy of the bookservice
# example whose buf.lock names a BSR commit that does not exist, so
# `buf generate` must fail. Passes when the e2e exits non-zero and the copy's
# gen/ is untouched. Before #1551 the e2e printed "passed" and gen/ was gone.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$DIR/../../../examples/protogen/bookservice"
work="$(mktemp -d "${TMPDIR:-/tmp}/protogen-selftest.XXXXXX")"
trap 'rm -rf "$work"' EXIT
cp -R "$SRC" "$work/bookservice"
ex="$work/bookservice"
sed -i.bak '/name: buf.build\/mcpkit\/protogen/{n;s/commit: .*/commit: 00000000000000000000000000000000/;}' "$ex/buf.lock"
grep -q 'commit: 00000000000000000000000000000000' "$ex/buf.lock" || { echo "selftest: could not break buf.lock" >&2; exit 1; }
before="$(cd "$ex/gen" && find . -type f | sort | xargs shasum)"

if PROTOGEN_E2E_EXAMPLE="$ex" bash "$DIR/scripts/test-e2e.sh" >"$work/out.log" 2>&1; then
    echo "selftest FAIL: test-e2e.sh exited 0 with a broken buf.lock"; tail -5 "$work/out.log"; exit 1
fi
if grep -q 'e2e: passed' "$work/out.log"; then
    echo "selftest FAIL: test-e2e.sh printed 'passed' after a failed generate"; exit 1
fi
after="$( [ -d "$ex/gen" ] && cd "$ex/gen" && find . -type f | sort | xargs shasum || true)"
if [ "$before" != "$after" ]; then
    echo "selftest FAIL: a failed generate changed or removed gen/"; exit 1
fi
echo "selftest: a failed generate fails the e2e and leaves gen/ intact"
