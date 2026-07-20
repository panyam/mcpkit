#!/usr/bin/env bash
# experimental events Telegram example tests
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$REPO_ROOT/examples/events/telegram" && go test ./... -count=1 -timeout 60s
