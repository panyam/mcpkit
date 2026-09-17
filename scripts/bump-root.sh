#!/usr/bin/env bash
# Points every sub-module's `require github.com/panyam/mcpkit` at version V,
# then tidies and re-verifies the dependency policy.
#
# Only the root self-reference is touched. Sub-module cross-references
# (github.com/panyam/mcpkit/ext/auth, /ext/ui) have their own independent tag
# timelines and must be bumped by hand to a real ext/* tag — or left alone when
# a `replace` directive is in play.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNNER="${RUNNER:-make}"
V="${V:-}"
MODS="${SUB_MODS_ALL:-}"

if [ -z "$V" ]; then
    echo "Usage: $RUNNER bump-root V=v0.1.22"
    exit 1
fi

for mod in $MODS; do
    [ -f "$mod/go.mod" ] || continue
    grep -q "github.com/panyam/mcpkit v" "$mod/go.mod" || continue
    echo "==> $mod/go.mod: require github.com/panyam/mcpkit $V"
    (cd "$mod" && go mod edit -require="github.com/panyam/mcpkit@$V")
done

# $(MAKE) rather than a bare `make`, so a nested run inherits the jobserver and
# any -n / -k the caller passed.
MAKE_CMD="${MAKE:-make}"
$MAKE_CMD -s tidy-all
$MAKE_CMD -s verify-submodule-deps
