#!/usr/bin/env bash
# lanternfall: the scripted check (an MCP client driving a player's page in
# Chromium) and, when ANTHROPIC_API_KEY is set, the LangGraph game master.
# Starts the Go server itself (see web/playwright.config.ts).
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$DIR/scripts/rooms-client.sh"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm exec playwright install chromium >/dev/null
pnpm exec playwright test "$@"
