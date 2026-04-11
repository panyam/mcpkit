#!/bin/bash
# Verifies that sub-module go.mod files require a real, tagged version of the
# root github.com/panyam/mcpkit module — not the v0.0.0 placeholder that used
# to exist before #189.
#
# Why: a sub-module's `require github.com/panyam/mcpkit v0.0.0` works locally
# thanks to the `replace ../../` directive, but downstream consumers cannot
# `go get github.com/panyam/mcpkit/ext/auth@vX` because Go ignores replace
# directives in non-main modules. The require line must point to a released
# tag so the module graph resolves for external users.
#
# Failure mode this catches: someone opens a sub-module go.mod and resets the
# require to v0.0.0 by accident (e.g., via go mod edit or a rebase) without
# noticing. CI should fail loudly.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SUBMODULES=(
    "ext/auth"
    "ext/ui"
)

fail=0
for sub in "${SUBMODULES[@]}"; do
    gomod="$REPO_ROOT/$sub/go.mod"
    if [ ! -f "$gomod" ]; then
        echo "MISSING: $gomod not found"
        fail=1
        continue
    fi

    # Extract the version required for github.com/panyam/mcpkit (the root module).
    # Look at the first non-indirect require line — we only care about the
    # direct dependency.
    version="$(awk '
        /^require[[:space:]]+github\.com\/panyam\/mcpkit[[:space:]]+v/ {
            print $3; exit
        }
        /^[[:space:]]+github\.com\/panyam\/mcpkit[[:space:]]+v/ {
            print $2; exit
        }
    ' "$gomod")"

    if [ -z "$version" ]; then
        echo "PASS: $sub does not require github.com/panyam/mcpkit (skipping)"
        continue
    fi

    if [ "$version" = "v0.0.0" ]; then
        echo "FAIL: $sub/go.mod requires github.com/panyam/mcpkit v0.0.0 (placeholder)"
        echo "      Bump to the current root tag. See CLAUDE.md 'Releasing Sub-Modules' for the release order."
        fail=1
        continue
    fi

    echo "PASS: $sub/go.mod requires github.com/panyam/mcpkit $version"
done

if [ $fail -ne 0 ]; then
    exit 1
fi

echo ""
echo "All sub-modules reference a real root version."
