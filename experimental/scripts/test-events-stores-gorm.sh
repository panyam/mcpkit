#!/usr/bin/env bash
# GORM-backed Webhook + Quota stores (sqlite + inmemory; no Docker)
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/events/stores/gorm" && go test ./... -count=1 -timeout 60s
