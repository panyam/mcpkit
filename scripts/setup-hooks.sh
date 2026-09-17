#!/usr/bin/env bash
# Installs the opt-in local git hooks. Both are bypassable with --no-verify, so
# neither is the real gate — check-no-binaries.sh in CI is.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

for hook in pre-push pre-commit; do
    cp "scripts/$hook-hook.sh" ".git/hooks/$hook"
    chmod +x ".git/hooks/$hook"
    echo "Installed .git/hooks/$hook -> scripts/$hook-hook.sh"
done
