#!/usr/bin/env bash
# experimental ext/events library tests
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/events" && go test ./... -count=1 -timeout 120s
