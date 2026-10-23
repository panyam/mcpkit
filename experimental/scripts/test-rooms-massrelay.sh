#!/usr/bin/env bash
# experimental ext/rooms/massrelay tests: the room bridge over a real massrelay
# relay, embedded and over its WebSocket.
#
# Not all under -race: servicekit's (v0.1.7, still in v0.1.9) grpcws BidiStreamConn.OnClose reads
# a message counter its forwarding goroutine updates atomically, so every
# WebSocket close is a reported race inside the relay. The adapter's own
# concurrency is covered by the embedded subtests, which open no WebSocket and
# do run under -race.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/rooms/massrelay"
go test ./... -count=1 -timeout 180s
go test . -count=1 -race -timeout 180s -run 'Transport_(Roster|Send|Ignores|Leave)[A-Za-z]*/embedded'
