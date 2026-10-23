#!/usr/bin/env bash
# experimental ext/rooms tests: the room bridge and its in-memory relay
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/rooms" && go test ./... -count=1 -race -timeout 120s
