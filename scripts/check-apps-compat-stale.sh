#!/usr/bin/env bash
# CI gate — conformance/apps/COMPAT.md is generated, so a diff after
# regenerating it means the committed copy is stale.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNNER="${RUNNER:-make}"

if git diff --exit-code conformance/apps/COMPAT.md; then
    exit 0
fi

echo "::error::conformance/apps/COMPAT.md is stale."
echo "::error::Run '$RUNNER refresh-apps-compat-report' locally and commit the diff."
exit 1
