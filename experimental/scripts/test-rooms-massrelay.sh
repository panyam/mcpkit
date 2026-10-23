#!/usr/bin/env bash
# experimental ext/rooms/massrelay tests: the room bridge over a real massrelay
# relay, embedded and over its WebSocket.
# All under -race since servicekit v0.1.10 fixed the OnClose counter race
# inside the relay (servicekit issue 41).
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/rooms/massrelay"
go test ./... -count=1 -race -timeout 240s
