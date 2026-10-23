#!/usr/bin/env bash
# experimental/integrations/roomsevents tests: the room bridge as an MCP Events
# server for its rooms, and the stdio channel front end Claude Code starts.
# Everything runs in-process (memroom relay, httptest bridge, piped stdio), so
# the whole module runs under -race.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/integrations/roomsevents"
go test ./... -count=1 -race -timeout 180s
