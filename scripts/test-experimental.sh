#!/usr/bin/env bash
# Every experimental/ module's test script, in order.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

SUITES="
test-agents
test-agents-clients-go
test-events
test-events-clients-go
test-events-stores-gorm
test-events-discord
test-events-telegram
"

for suite in $SUITES; do
    bash "experimental/scripts/$suite.sh"
done
