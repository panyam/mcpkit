#!/usr/bin/env bash
# any-frontend: drive all four Views through the /host page in Chromium.
# Starts the Go server itself (see web/playwright.config.ts).
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm exec playwright install chromium
pnpm exec playwright test
