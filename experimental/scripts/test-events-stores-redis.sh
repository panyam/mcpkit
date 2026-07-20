#!/usr/bin/env bash
# Redis pubsub Emitter sub-module (miniredis; no Docker)
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/events/stores/redis" && go test ./... -count=1 -timeout 60s
