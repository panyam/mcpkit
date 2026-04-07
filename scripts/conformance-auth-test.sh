#!/bin/bash
# Runs the MCP Auth conformance test suite against the mcpkit client.
# These are CLIENT-side tests — they spin up a mock AS and test that our client
# correctly handles: PRM discovery, AS metadata discovery, PKCE, scope handling.
#
# Requires:
#   - Node.js/npx for @modelcontextprotocol/conformance
#   - mcpkit/auth OAuth client implementation (Phase 3C-3E)
#
# Usage:
#   bash scripts/conformance-auth-test.sh              # Run full auth suite
#   bash scripts/conformance-auth-test.sh <scenario>   # Run single scenario
#
# Environment:
#   CONF_VERBOSE  — set to 1 for verbose output
set -euo pipefail

SCENARIO="${1:-}"
BASELINE="conformance/baseline.yml"

# Build the testclient binary (separate Go module).
# The conformance runner invokes this binary with the server URL as an argument.
echo "Building testclient..."
(cd cmd/testclient && go build -buildvcs=false -o ../../bin/testclient .) || exit 1
CLIENT_CMD="./bin/testclient"

# Check prerequisites
if ! command -v npx &>/dev/null; then
    echo "FAIL: npx not found. Install Node.js 22+ to run conformance tests."
    exit 1
fi

if [ ! -d "cmd/testclient" ]; then
    echo "SKIP: cmd/testclient not yet implemented (requires mcpkit/auth Phase 3C-3E)"
    echo ""
    echo "Auth conformance scenarios are tracked in $BASELINE under client-auth:"
    echo "  22 scenarios total (14 required, 2 back-compat, 3 extensions, 3 draft)"
    echo ""
    echo "To run manually once implemented:"
    echo "  npx @modelcontextprotocol/conformance client --suite auth --command \"$CLIENT_CMD\""
    exit 0
fi

# Build conformance command
CONF_CMD="npx -y @modelcontextprotocol/conformance client --suite auth --command \"$CLIENT_CMD\""

if [ -f "$BASELINE" ]; then
    CONF_CMD="$CONF_CMD --expected-failures $BASELINE"
fi

if [ -n "$SCENARIO" ]; then
    CONF_CMD="$CONF_CMD --scenario $SCENARIO"
fi

if [ "${CONF_VERBOSE:-}" = "1" ]; then
    CONF_CMD="$CONF_CMD --verbose"
fi

echo "=== Running MCP Auth conformance suite ==="
echo "Command: $CONF_CMD"
echo ""

set +e
eval "$CONF_CMD"
EXIT_CODE=$?
set -e

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
    echo "=== AUTH CONFORMANCE TESTS PASSED ==="
else
    echo "=== AUTH CONFORMANCE TESTS FAILED (exit code $EXIT_CODE) ==="
    echo ""
    echo "If these are expected failures, add them to $BASELINE under client-auth:"
fi

exit "$EXIT_CODE"
