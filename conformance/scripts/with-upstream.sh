#!/usr/bin/env bash
# Runs one of the repo-root conformance scripts, after checking that the
# upstream conformance clone is actually there.
#
# That check was written out four times — twice in conformance/Makefile and
# twice in conformance/justfile — for two callers.
#
# Usage: with-upstream.sh <repo-root script path>
set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
BASE="${MCPCONFORMANCE_BASE_PATH:-$REPO_ROOT/../conf-upstream-main}"
SCRIPT="${1:?usage: with-upstream.sh <script path relative to repo root>}"

if [ ! -d "$BASE" ]; then
    echo "MCPCONFORMANCE_BASE_PATH=$BASE does not exist."
    echo "Clone https://github.com/modelcontextprotocol/conformance there:"
    echo "  git clone https://github.com/modelcontextprotocol/conformance.git $BASE"
    echo "Or set MCPCONFORMANCE_BASE_PATH=<path-to-clone>."
    exit 1
fi

cd "$REPO_ROOT"
MCPCONFORMANCE_BASE_PATH="$BASE" exec bash "$SCRIPT"
