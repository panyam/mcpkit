#!/usr/bin/env bash
# Points every sub-module's `require github.com/panyam/mcpkit` at version V,
# and every tagged sub-module's requires on other tagged sub-modules at V too
# (scripts/bump-pins.sh), then tidies and re-verifies the dependency policy.
#
# Sibling pins move because every tagged module gets the same tag in one
# `make tag-push`. Leaving them a release behind broke v0.7.0's stores/gorm
# for anyone who fetched it without also requiring events@v0.7.0.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNNER="${RUNNER:-make}"
V="${V:-}"
MODS="${SUB_MODS_ALL:-}"

if [ -z "$V" ]; then
    echo "Usage: $RUNNER bump-root V=v0.1.22"
    exit 1
fi

V="$V" SUB_MODS_ALL="$MODS" bash scripts/bump-pins.sh "$PWD"

# $(MAKE) rather than a bare `make`, so a nested run inherits the jobserver and
# any -n / -k the caller passed.
MAKE_CMD="${MAKE:-make}"
$MAKE_CMD -s tidy-all
$MAKE_CMD -s verify-submodule-deps
