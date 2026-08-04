#!/usr/bin/env bash
# experimental ext/agents Go client SDK tests
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/agents/clients/go" && go test ./... -count=1 -timeout 60s
