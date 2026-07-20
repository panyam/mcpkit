#!/usr/bin/env bash
# Shared setup for the runner-agnostic experimental test scripts.
# Source at the top of each: sets EXPERIMENTAL_DIR + REPO_ROOT.
EXPERIMENTAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$EXPERIMENTAL_DIR/.." && pwd)"
export EXPERIMENTAL_DIR REPO_ROOT
