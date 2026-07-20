#!/usr/bin/env bash
# ext/ui: JS bridge unit tests (requires pnpm + Node.js). Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/assets" && pnpm install --frozen-lockfile --silent && pnpm test
