#!/usr/bin/env bash
# Builds every binary in the example into bin/, without Docker.
#
# push-server is deliberately absent: it stays a reference for the
# production-shape HTTPSource pattern and is run with `go -C push-server run .`.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# module dir -> output path, relative to that module
BUILDS=(
    "event-server:../bin/event-server"
    "poller:../bin/poller"
    "webhook:../bin/webhook"
    "streamer:../bin/streamer"
    "inject:../bin/inject"
    "drivers/synth:../../bin/synth"
    "evctl:../bin/evctl"
    "walkthrough:../bin/walkthrough"
)

for entry in "${BUILDS[@]}"; do
    go -C "${entry%%:*}" build -o "${entry#*:}" ./
done
