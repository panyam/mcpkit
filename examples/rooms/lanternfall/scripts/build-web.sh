#!/usr/bin/env bash
# lanternfall: rebuild static/app.js from web/src. Commit the result, since Go
# embeds static/ and `go run .` needs no Node.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$DIR/scripts/rooms-client.sh"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm build
