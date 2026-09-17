#!/usr/bin/env bash
# Injects one event from the host into a tenant's stream.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

TENANT="${TENANT:-}"
EVENT="${EVENT:-chat.message}"
TEXT="${TEXT:-hello from \`just inject\`}"

if [ -z "$TENANT" ]; then
    usage_for inject "TENANT=A|B|C" "[EVENT=<name>]" "[TEXT='...']"
fi

# TEXT reaches us as one argument already, so the Makefile's shellquote
# function (which escaped it into a single quoted word) has nothing left to do.
exec go -C inject run . --tenant "$(tenant_realm "$TENANT")" --event "$EVENT" --text "$TEXT"
