#!/usr/bin/env bash
# Tags the root and every published sub-module at V. Local only — it prints the
# push command rather than running it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNNER="${RUNNER:-make}"
V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: $RUNNER tag V=v0.0.11"
    exit 1
fi

echo "Tagging $V across all modules..."
git tag -a "$V" -m "$V"

refs="$V"
for mod in $MODS; do
    echo "  $mod/$V"
    git tag -a "$mod/$V" -m "$mod/$V"
    refs="$refs $mod/$V"
done

echo ""
echo "Tags created locally. Push with:"
echo "  git push origin $refs"
