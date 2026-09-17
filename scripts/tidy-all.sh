#!/usr/bin/env bash
# `go mod tidy` in the root and every module that has a go.mod.
#
# Required after touching core/ imports, or sub-module go.sum files drift and
# CI fails in a module you did not edit.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "==> tidy root"
go mod tidy

MODS="${SUB_MODS_ALL:-}" scripts/submodule-loop.sh tidy go mod tidy
