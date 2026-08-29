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
#
# Modules whose go.mod carries a `replace` pointing outside this repository are
# skipped: they need a sibling checkout that exists on a developer's machine but
# not on a CI runner, so building one here reports the missing directory rather
# than anything about the code. Skips are printed, never silent, so the gate's
# coverage stays legible.
set -uo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

# external_replace prints the first replace target in $1/go.mod that resolves
# outside $ROOT and returns 0; returns 1 when every target is in-tree.
external_replace() {
    local dir=$1 target parent resolved
    while read -r target; do
        [ -n "$target" ] || continue
        # Resolve against the module dir. A target whose parent does not exist
        # is, for our purposes, as unavailable as one outside the repo.
        parent=$(cd "$dir" 2>/dev/null && cd "$(dirname "$target")" 2>/dev/null && pwd)
        if [ -z "$parent" ]; then
            echo "$target"
            return 0
        fi
        resolved="$parent/$(basename "$target")"
        case "$resolved" in
            "$ROOT"|"$ROOT"/*) ;;
            *) echo "$target"; return 0 ;;
        esac
    done < <(grep -oE '=>[[:space:]]+\.\.[^[:space:]]*' "$dir/go.mod" 2>/dev/null | awk '{print $2}')
    return 1
}

rc=0
built=0
skipped=0
while IFS= read -r mod; do
    dir=$(dirname "$mod")
    if ext=$(external_replace "$dir"); then
        echo "SKIP: $dir (replace -> $ext is outside this repo)"
        skipped=$((skipped + 1))
        continue
    fi
    built=$((built + 1))
    # `go build ./...` in a directory whose name collides with a produced
    # binary reports "build output already exists and is a directory". That is
    # not a compile error, so build to a discard target instead.
    if ! out=$(cd "$dir" && go build -o /dev/null ./... 2>&1); then
        echo "FAIL: $dir"
        echo "$out" | sed 's/^/    /'
        rc=1
    fi
done < <(find examples -name go.mod | sort)

if [ $rc -ne 0 ]; then
    echo
    echo "check-examples-build: one or more example modules do not compile."
    echo "Examples are published as reference code; a broken one is a broken doc."
    exit 1
fi

echo "check-examples-build: $built example modules compile, $skipped skipped."
