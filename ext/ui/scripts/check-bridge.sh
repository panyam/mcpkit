#!/usr/bin/env bash
# ext/ui: typecheck the bridge and fail if the committed mcp-app-bridge.js is
# stale against mcp-app-bridge.ts. Go embeds the committed .js, so a .ts edit
# without a rebuild ships the old bridge. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/assets"
pnpm install --frozen-lockfile --silent
pnpm check
before="$(mktemp)"
trap 'rm -f "$before"' EXIT
cp mcp-app-bridge.js "$before"
pnpm build
if ! cmp -s "$before" mcp-app-bridge.js; then
  echo "mcp-app-bridge.js was stale against mcp-app-bridge.ts. It has been rebuilt: commit it." >&2
  exit 1
fi
