#!/usr/bin/env bash
# Pushes what `make tag` (scripts/tag-all.sh) created: the branch, then the
# root tag, then the sub-module tags.
#
# The branch goes first. tag-all leaves two release commits on it (the
# stripped tree the tags point at, then the restore, #1488), and a rejected
# branch push should stop the run before any tag is public.
#
# The root tag is pushed BY ITSELF, ahead of the sub-module tags. GitHub does
# not raise push events for a tag push carrying many refs at once, and this
# repo pushes twenty. The symptom is silent: the release succeeds, and the
# workflows keyed on `tags: ['v*']` simply never run. Both of ours were in that
# state from v0.4.0 through v0.6.0 -- publish-images.yml had zero runs ever, and
# vulncheck.yml only ever fired on its schedule, which quietly falsified
# DEPENDENCY_POLICY.md's claim that the audit runs before every tagged release.
# Two pushes, not one, is the whole fix. See #1391.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"
[ -n "$V" ] || { echo "usage: V=vX.Y.Z tag-push.sh" >&2; exit 1; }

git push origin HEAD
git push origin "$V"
refs=""
for mod in $MODS; do refs="$refs $mod/$V"; done
# shellcheck disable=SC2086
git push origin $refs
