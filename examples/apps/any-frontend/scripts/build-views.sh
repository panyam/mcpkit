#!/usr/bin/env bash
# any-frontend: build the three upstream-runtime Views and the /host page from
# web/ into views/*.html. Commit the output so `go run .` needs no Node.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm build
