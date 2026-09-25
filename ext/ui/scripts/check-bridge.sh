#!/usr/bin/env bash
# ext/ui: typecheck the bridge and the extras, and fail if the committed
# mcp-app-bridge.js or mcp-app-extras.js is stale against its .ts. Go embeds
# the committed bridge .js, and Views vendor the extras .js, so a .ts edit
# without a rebuild ships old code. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/assets"
pnpm install --frozen-lockfile --silent
pnpm check
bundles="mcp-app-bridge.js mcp-app-extras.js"
snap="$(mktemp -d)"
trap 'rm -rf "$snap"' EXIT
for f in $bundles; do cp "$f" "$snap/$f"; done
pnpm build
stale=0
for f in $bundles; do
  if ! cmp -s "$snap/$f" "$f"; then
    echo "$f was stale against its .ts source. It has been rebuilt: commit it." >&2
    stale=1
  fi
done
exit "$stale"
