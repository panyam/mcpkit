#!/usr/bin/env bash
# lanternfall: typecheck web/ and fail if the committed static/app.js is stale
# against its sources. Go embeds the committed file, so a source edit without
# a rebuild ships the old page.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$DIR/scripts/rooms-client.sh"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm check
snap="$(mktemp)"
trap 'rm -f "$snap"' EXIT
cp ../static/app.js "$snap"
pnpm build >/dev/null
if ! cmp -s "$snap" ../static/app.js; then
  echo "static/app.js was stale against web/. It has been rebuilt: commit it." >&2
  exit 1
fi
