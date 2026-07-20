#!/usr/bin/env bash
# All E2E tests (auth, apps — no Docker). Runner-agnostic.
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/tests/e2e" && go test ./... -count=1 -timeout 60s
