#!/usr/bin/env bash
# Tags the root and every published sub-module at V. Local only — it prints the
# push command rather than running it.
#
# The tags go on a commit with the in-repo replaces stripped, and a second
# commit puts them back (scripts/release-replaces.sh, #1488):
#
#   release: installable tree for V            <- every tag points here
#   release: restore in-repo replaces after V  <- the branch's new head
#
# Push the branch with the tags, or main lags behind its own release.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNNER="${RUNNER:-make}"
V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: $RUNNER tag V=v0.0.11"
    exit 1
fi

# The strip commit takes every tracked change with it, so start clean.
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "tag-all: working tree has uncommitted changes; commit or stash them first" >&2
    exit 1
fi

SUB_MODS_TO_TAG="$MODS" bash scripts/release-replaces.sh strip
git commit -q -a -m "release: installable tree for $V" \
    -m "Strips the in-repo replace directives so go install pkg@$V works. The next commit restores them (#1488)."

echo "Tagging $V across all modules..."
git tag -a "$V" -m "$V"

refs="$V"
for mod in $MODS; do
    echo "  $mod/$V"
    git tag -a "$mod/$V" -m "$mod/$V"
    refs="$refs $mod/$V"
done

bash scripts/release-replaces.sh restore
git commit -q -a -m "release: restore in-repo replaces after $V"

echo ""
echo "Tags created locally. Push with:"
echo "  git push origin HEAD"
echo "  git push origin $refs"
