#!/usr/bin/env bash
# Exercises bump-pins.sh against a throwaway fixture tree: a root module, two
# tagged sub-modules where one requires the other, and an untagged example
# that requires a tagged sub-module. After V=v0.9.0:
#   - every module's root require is v0.9.0
#   - the tagged sub-module's sibling require is v0.9.0
#   - the untagged example's sibling require is unchanged
#   - a tagged module's v0.0.0 placeholder sibling pin (the non-library
#     policy in verify-submodule-deps.sh) stays a placeholder
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

mkmod() { # dir module-path requires...
    local dir="$1" path="$2"; shift 2
    mkdir -p "$T/$dir"
    { echo "module $path"; echo; echo "go 1.26"; echo; echo "require ("
      for r in "$@"; do echo "	$r"; done; echo ")"; } > "$T/$dir/go.mod"
}
printf 'module github.com/panyam/mcpkit\n\ngo 1.26\n' > "$T/go.mod"
mkmod ext/a   github.com/panyam/mcpkit/ext/a "github.com/panyam/mcpkit v0.8.0" "github.com/panyam/mcpkit/ext/b v0.8.0"
mkmod ext/b   github.com/panyam/mcpkit/ext/b "github.com/panyam/mcpkit v0.8.0"
mkmod tests/e2e github.com/panyam/mcpkit/tests/e2e "github.com/panyam/mcpkit v0.8.0" "github.com/panyam/mcpkit/ext/a v0.0.0"
# Real modules carry replace blocks, whose lines also start with a sibling path.
printf '\nreplace (\n\tgithub.com/panyam/mcpkit => ../..\n\tgithub.com/panyam/mcpkit/ext/a => ../../ext/a\n)\n' >> "$T/tests/e2e/go.mod"
mkmod examples/x github.com/panyam/mcpkit/examples/x "github.com/panyam/mcpkit v0.8.0" "github.com/panyam/mcpkit/ext/b v0.8.0"

V=v0.9.0 SUB_MODS_ALL="ext/a ext/b tests/e2e examples/x" SUB_MODS_TO_TAG="ext/a ext/b tests/e2e" \
    bash "$HERE/bump-pins.sh" "$T" >/dev/null

fail=0
want() { # dir module version
    local got
    got="$(cd "$T/$1" && go mod edit -json | python3 -c "import json,sys;d=json.load(sys.stdin);print(next((r['Version'] for r in d.get('Require') or [] if r['Path']=='$2'),'absent'))")"
    if [ "$got" = "$3" ]; then echo "ok    $1 requires $2 $got"; else echo "FAIL  $1 requires $2 $got, want $3"; fail=1; fi
}
want ext/a      github.com/panyam/mcpkit        v0.9.0
want ext/a      github.com/panyam/mcpkit/ext/b  v0.9.0
want ext/b      github.com/panyam/mcpkit        v0.9.0
want examples/x github.com/panyam/mcpkit        v0.9.0
want examples/x github.com/panyam/mcpkit/ext/b  v0.8.0
want tests/e2e  github.com/panyam/mcpkit        v0.9.0
want tests/e2e  github.com/panyam/mcpkit/ext/a  v0.0.0
[ "$fail" = 0 ] && echo "bump-pins-selftest: ok" || { echo "bump-pins-selftest: FAILED"; exit 1; }
