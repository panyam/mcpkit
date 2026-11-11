#!/usr/bin/env bash
# Builds and vets every module in SUB_MODS_TO_TAG, and runs the tests of each
# one that no other CI job tests (#1486).
#
# cmd/mcpskills shipped in v0.6.0 unable to compile, and nothing in CI built
# it. Since #1488 main keeps a `replace` for every in-repo dependency, so a
# module built here compiles against main's siblings, the same tree the next
# tag will hold. Vet type-checks _test.go files too, which `go build` skips.
#
# TESTED_ELSEWHERE lists modules whose tests another job already runs. Every
# other tagged module has its tests run here, so a module added to
# SUB_MODS_TO_TAG is tested by default. An entry fails the check when its
# module leaves SUB_MODS_TO_TAG, when its job no longer exists, or when no
# workflow or script names the module any more.
#
#   check-tagged-modules.sh
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

MODS="${SUB_MODS_TO_TAG:-}"
if [ -z "$MODS" ]; then
    MODS="$(sed -n '/^SUB_MODS_TO_TAG *:=/,/^$/p' Makefile | tr -d '\\' | tr -s ' \t' '\n' \
        | grep -v -e '^SUB_MODS_TO_TAG$' -e '^:=$' -e '^$' || true)"
fi
[ -n "$MODS" ] || { echo "check-tagged-modules: SUB_MODS_TO_TAG is empty" >&2; exit 2; }

# module:job. tests/keycloak needs a live Keycloak, so its tests run only in
# keycloak-interop.yml (job keycloak).
TESTED_ELSEWHERE="
ext/auth:test-auth
tests/e2e:test-auth
ext/ui:test-ui
experimental/ext/events:test-events
experimental/ext/events/clients/go:test-events
experimental/ext/rooms:test-rooms
experimental/ext/rooms/massrelay:test-rooms
experimental/integrations/roomsevents:test-rooms
experimental/ext/agents:test-agents
experimental/ext/agents/clients/go:test-agents
tests/keycloak:keycloak
"

rc=0
fail() { echo "FAIL: $*"; rc=1; }

elsewhere=""
for entry in $TESTED_ELSEWHERE; do
    mod="${entry%%:*}"
    job="${entry#*:}"
    case " $(echo $MODS) " in
        *" $mod "*) ;;
        *) fail "TESTED_ELSEWHERE names $mod, which is not in SUB_MODS_TO_TAG"; continue ;;
    esac
    if ! grep -qE "^  $job:" .github/workflows/*.yml; then
        fail "TESTED_ELSEWHERE says $job tests $mod, but no workflow has a job named $job"
        continue
    fi
    # experimental/scripts reach their modules through $EXPERIMENTAL_DIR, so
    # match the path below experimental/, ending at a quote, space or line end.
    if ! grep -rqE "${mod#experimental/}([\"' ]|$)" .github/workflows experimental/scripts ext/ui/scripts; then
        fail "TESTED_ELSEWHERE says $job tests $mod, but no workflow or script names $mod"
        continue
    fi
    elsewhere="$elsewhere $mod "
done

built=0
tested=0
for mod in $MODS; do
    [ -f "$mod/go.mod" ] || { fail "$mod is in SUB_MODS_TO_TAG and has no go.mod"; continue; }
    built=$((built + 1))
    if ! out=$(cd "$mod" && go build -o /dev/null ./... 2>&1); then
        fail "$mod does not build"
        echo "$out" | sed 's/^/    /'
        continue
    fi
    if ! out=$(cd "$mod" && go vet ./... 2>&1); then
        fail "$mod does not vet (includes _test.go)"
        echo "$out" | sed 's/^/    /'
        continue
    fi
    case "$elsewhere" in *" $mod "*) continue ;; esac
    tested=$((tested + 1))
    if ! out=$(cd "$mod" && go test ./... -count=1 -timeout 120s 2>&1); then
        fail "$mod tests"
        echo "$out" | grep -v -e '^ok ' -e '^?' | sed 's/^/    /'
    fi
done

if [ $rc -ne 0 ]; then
    echo
    echo "check-tagged-modules: a tagged module would ship broken."
    exit 1
fi
echo "check-tagged-modules: $built tagged modules build and vet, $tested tested here."
