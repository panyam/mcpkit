#!/usr/bin/env bash
# Wipes the SHARED docker/backends stack: containers, named volumes, and the
# bind-mounted data dir under docker/backends/data/.
#
# The events stack has owned its own postgres/redis since the compose merge, so
# this is no longer part of resetting events — `make clean` / `just clean` does
# that. What it still affects is examples/agents, which shares that instance.
# Hence the confirm prompt, which is the reason this is a script rather than a
# recipe one-liner.
#
# Called by `make clean-backends` and `just clean-backends`. RUNNER names the
# caller so the message points at the right sibling command.
set -euo pipefail

RUNNER="${RUNNER:-make}"
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKENDS_DIR="$DEMO_DIR/../../../docker/backends"

echo "[clean-backends] events owns its own postgres/redis volumes since the compose merge."
echo "[clean-backends] this wipes docker/backends, which examples/agents shares."
echo "[clean-backends] to reset events instead, use '$RUNNER clean'."
printf 'proceed? [y/N] '
# EOF here (non-interactive caller) fails the read, and set -e stops us before
# anything is deleted. That is the direction we want to fail in.
read -r ans
[ "$ans" = "y" ] || exit 1

docker compose -f "$BACKENDS_DIR/docker-compose.yml" down -v 2>/dev/null || true
rm -rf "${BACKENDS_DIR:?}/data/"
