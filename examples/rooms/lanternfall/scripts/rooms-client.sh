#!/usr/bin/env bash
# lanternfall: build the TypeScript page client the page links to
# (experimental/ext/rooms/ts), since web/package.json uses it from the tree
# rather than from npm. Sourced by the other scripts.
set -eu
ROOMS_TS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../experimental/ext/rooms/ts" && pwd)"
(cd "$ROOMS_TS" && pnpm install --frozen-lockfile --silent && pnpm build >/dev/null)
