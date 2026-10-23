#!/usr/bin/env bash
# experimental ext/rooms/ts tests: the TypeScript page client (joinToolRoom)
# against the Go room bridge on a real massrelay relay. The tests start the Go
# side themselves (go run of rooms/massrelay/internal/cmd/teststack), so this
# needs both Go and pnpm.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/rooms/ts"
pnpm install --frozen-lockfile --silent
pnpm check
pnpm test
# What npm would ship: the built entry points, no tests or sources.
pnpm build
pnpm check:pack
