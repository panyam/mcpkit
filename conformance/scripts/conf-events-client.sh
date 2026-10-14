#!/usr/bin/env bash
# MCP Events client conformance — informational (exit 0 regardless of check
# results). Drives the Go events client SDK through the four events-client-*
# scenarios (conformance PR 541, stacked on the server suite's PR 521).
#
# The harness plays the events server and grades what the client sends back.
# The client is experimental/ext/events/clients/go/cmd/conformance-client, a
# thin driver that only calls what the SDK offers, so a red row is an SDK gap
# rather than a driver choice to patch.
#
# INFO for the same reason as testconf-events: the rows quote the design
# sketch while they are re-extracted against SEP-3415 (issue 1502).
#
# EVENTS_CLIENT_DRIVER overrides the driver binary. Pointing it at something
# that cannot speak MCP (/bin/false) turns every scenario red, which is how to
# confirm the suite is grading the driver at all.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_EVENTS_CLIENT_PATH \
    "Run 'make conf-events-setup' to create it, or set MCPCONFORMANCE_EVENTS_CLIENT_PATH=<path-to-checkout>." \
    "Expects the feat/events-client-conformance branch of https://github.com/panyam/mcpconformance."
build_conf_dist MCPCONFORMANCE_EVENTS_CLIENT_PATH

OUT=$(mktemp -d -t conf-events-client.XXXXXX)

# Built into the scratch dir, never into the module: a bare `go build` there
# drops a binary that `git add -A` would sweep into a commit.
DRIVER="${EVENTS_CLIENT_DRIVER:-}"
if [ -z "${DRIVER}" ]; then
    DRIVER="$OUT/events-conformance-client"
    (cd "${REPO_ROOT}/experimental/ext/events/clients/go" && \
        go build -o "$DRIVER" ./cmd/conformance-client) || exit 1
fi
echo "Driver ${DRIVER}, scratch dir $OUT"

SCENARIOS="events-client-poll events-client-push events-client-webhook events-client-no-extension"
for S in ${SCENARIOS}; do
    (cd "${MCPCONFORMANCE_EVENTS_CLIENT_PATH}" && \
        node dist/index.js client \
            --command "${DRIVER}" \
            --scenario "${S}" \
            -o "$OUT/checks-${S}" > "$OUT/runner-${S}.log" 2>&1)
    SRC=$?
    SUMMARY=$(grep -E "Passed:" "$OUT/runner-${S}.log" | tail -1 | sed 's/\x1b\[[0-9;]*m//g')
    echo "  ${S}: ${SUMMARY:-runner exited ${SRC} (see $OUT/runner-${S}.log)}"
done

echo "testconf-events-client: INFORMATIONAL, exiting 0. Runner logs in $OUT"
exit 0
