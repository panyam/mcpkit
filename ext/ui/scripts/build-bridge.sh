#!/usr/bin/env bash
# ext/ui: compile mcp-app-bridge.ts → .js (requires pnpm + TypeScript). Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/assets" && pnpm install --frozen-lockfile && pnpm build
