#!/usr/bin/env bash
# Build every module under examples/.
#
# examples/ holds 58 independent modules. `make test-examples` runs an explicit
# allowlist of ~10 that have tests, and nothing else in CI compiles the rest, so
# an API change in core/ could break an example and stay broken indefinitely.
# examples/apps/react/server did exactly that: it stopped compiling in c45ccb78
# (the sealed-interface ToolResponse migration) and was found by hand months
# later, while every CI run stayed green.
#
# This is a build gate, not a test gate. It is cheap and its failure is
# unambiguous, matching check-no-binaries.sh and check-ext-isolation.sh.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

rc=0
count=0
while IFS= read -r mod; do
    dir=$(dirname "$mod")
    count=$((count + 1))
    # `go build ./...` in a directory whose name collides with a produced
    # binary reports "build output already exists and is a directory". That is
    # not a compile error, so build to a discard target instead.
    if ! out=$(cd "$dir" && go build -o /dev/null ./... 2>&1); then
        echo "FAIL: $dir"
        echo "$out" | sed 's/^/    /'
        rc=1
    fi
done < <(find examples -name go.mod)

if [ $rc -ne 0 ]; then
    echo
    echo "check-examples-build: one or more example modules do not compile."
    echo "Examples are published as reference code; a broken one is a broken doc."
    exit 1
fi

echo "check-examples-build: all $count example modules compile."
