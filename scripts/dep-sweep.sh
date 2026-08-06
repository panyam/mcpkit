#!/usr/bin/env bash
# Repo-wide dependency sweep: update every Go module, then reconcile the whole
# tree in one commit.
#
# Why this exists rather than per-directory Dependabot PRs
# --------------------------------------------------------
# Dependabot opens one PR per directory and cannot touch modules outside its
# configured globs. This repo excludes examples/ and tests/ from that config,
# but those modules depend on the published ones through `replace` directives,
# so a Dependabot bump leaves their go.mod stale and CI fails with
# "updates to go.mod needed". Observed on PR 1225: a 9-directory bump required
# 57 further modules to be tidied in lock-step.
#
# DEPENDENCY_POLICY.md requires lock-step pins for exactly this reason. Only a
# sweep that sees every module at once can deliver that.
#
# Usage:
#   scripts/dep-sweep.sh [patch|minor]
#
#   patch  — go get -u=patch, low risk, no minor-version churn
#   minor  — go get -u, the real sweep (default)
set -euo pipefail

MODE="${1:-minor}"
case "$MODE" in
    patch) FLAG="-u=patch" ;;
    minor) FLAG="-u" ;;
    *) echo "dep-sweep: mode must be 'patch' or 'minor', got '$MODE'" >&2; exit 2 ;;
esac

cd "$(dirname "$0")/.."

# Every module, not just the published surface: the whole point is that the
# excluded ones are what go stale.
mods="$(find . -name go.mod -not -path '*/node_modules/*' -not -path '*/.claude/*' \
    | sed 's|^\./||;s|/go.mod$||' | sort)"

failed=""
count=0
for mod in $mods; do
    [ "$mod" = "go.mod" ] && mod="."
    count=$((count + 1))
    echo "==> go get $FLAG $mod"
    # A module that cannot resolve is recorded and the sweep continues; one bad
    # module should not hide updates for the other 87.
    (cd "$mod" && go get $FLAG ./...) || failed="$failed $mod"
done

# `go get -u` runs per module against that module's own graph, so it routinely
# lands modules on different patch levels and INTRODUCES divergence. Unification
# is what turns a pile of independent updates into one consistent tree, and it
# is the whole reason this is a sweep rather than N per-directory bumps.
echo ""
echo "==> unify divergent pins to the maximum already in the tree"
python3 scripts/check_dep_consistency.py --unify

echo ""
echo "==> tidy-all (reconciles every module, including the ones Dependabot cannot reach)"
just tidy-all

# One-way ratchet: drop baseline entries the sweep just resolved, never add.
echo ""
echo "==> prune the divergence baseline"
python3 scripts/check_dep_consistency.py --prune-baseline

echo ""
if [ -n "$failed" ]; then
    echo "=== sweep completed with failures in:$failed ==="
    echo "The tree is still tidied; review the listed modules by hand."
    exit 1
fi
echo "=== sweep clean across $count modules (mode: $MODE) ==="
