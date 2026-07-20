#!/usr/bin/env bash
# experimental ext/events Go client SDK tests
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/events/clients/go" && go test ./... -count=1 -timeout 60s
