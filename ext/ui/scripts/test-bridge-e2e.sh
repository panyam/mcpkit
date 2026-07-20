#!/usr/bin/env bash
# ext/ui: Playwright bridge integration tests (requires Chromium). Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"
cp assets/mcp-app-bridge.js tests/playwright/mcp-app-bridge.js
cd tests/playwright && pnpm install --frozen-lockfile --silent && pnpm test
