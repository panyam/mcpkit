#!/usr/bin/env bash
# Repoints intra-repo requires at version V, under repo root $1 (default: the
# repository this script lives in). Edits go.mod files only. bump-root.sh
# calls it, then tidies and re-verifies.
#
# Two passes:
#   1. Every module in SUB_MODS_ALL: `require github.com/panyam/mcpkit` -> V.
#   2. Every module in SUB_MODS_TO_TAG: each require of another tagged
#      mcpkit module -> V. All tagged modules get the same tag in one
#      `make tag-push`, so the siblings resolve once the tags are pushed.
#      Without this, a tagged module that uses sibling API added this release
#      fails to compile for anyone who `go get`s it alone (v0.7.0: stores/gorm
#      used events API that events@v0.6.0 lacked).
# A sibling pinned at the v0.0.0 placeholder stays a placeholder: the
# non-library policy in verify-submodule-deps.sh allows it for tests/*,
# which resolve siblings only through replace.
# Untagged modules (examples) keep their sibling pins. Their `replace`
# directives make the pins irrelevant locally, and nobody consumes them.
set -euo pipefail
ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
V="${V:?usage: V=vX.Y.Z bump-pins.sh [root]}"
MODS="${SUB_MODS_ALL:-}"
cd "$ROOT"
# SUB_MODS_TO_TAG comes from the Makefile's export. Parse it from the
# Makefile when run another way, as check_dep_consistency.py does.
TAGGED="${SUB_MODS_TO_TAG:-}"
if [ -z "$TAGGED" ] && [ -f Makefile ]; then
    TAGGED="$(sed -n '/^SUB_MODS_TO_TAG *:=/,/^$/p' Makefile | tr -d '\\' | tr -s ' \t' '\n' | grep -v -e '^SUB_MODS_TO_TAG$' -e '^:=$' -e '^$' || true)"
fi
for mod in $MODS; do
    [ -f "$mod/go.mod" ] || continue
    grep -q "github.com/panyam/mcpkit v" "$mod/go.mod" || continue
    echo "==> $mod/go.mod: require github.com/panyam/mcpkit $V"
    (cd "$mod" && go mod edit -require="github.com/panyam/mcpkit@$V")
done

is_tagged() {
    local m
    for m in $TAGGED; do [ "$m" = "$1" ] && return 0; done
    return 1
}

for mod in $TAGGED; do
    [ -f "$mod/go.mod" ] || continue
    for dep in $(awk '
        $1 ~ /^github\.com\/panyam\/mcpkit\// && $2 ~ /^v/ && $2 !~ /^v0\.0\.0/ {print $1}
        $1 == "require" && $2 ~ /^github\.com\/panyam\/mcpkit\// && $3 ~ /^v/ && $3 !~ /^v0\.0\.0/ {print $2}
    ' "$mod/go.mod" | sed 's|^github.com/panyam/mcpkit/||' | sort -u); do
        is_tagged "$dep" || continue
        echo "==> $mod/go.mod: require github.com/panyam/mcpkit/$dep $V"
        (cd "$mod" && go mod edit -require="github.com/panyam/mcpkit/$dep@$V")
    done
done
