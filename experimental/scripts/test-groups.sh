#!/usr/bin/env bash
# experimental ext/groups tests (progressive-disclosure-wg groups draft)
# Runner-agnostic: experimental + root recipes call this directly.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
cd "$EXPERIMENTAL_DIR/ext/groups" && go test ./... -count=1 -timeout 60s
