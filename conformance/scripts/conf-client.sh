#!/usr/bin/env bash
# Client conformance — drives cmd/testclient through upstream's full client
# suite (core + auth + backcompat + extensions + draft), the same set
# tier-check's --client-cmd runs. Expected failures live in
# conformance/baseline.yml (client section); the upstream runner exits 0
# only when nothing outside that list fails and no listed entry passes
# (stale-entry guard).
#
# Runner-agnostic: the Makefile + justfile `testconf-client` recipes call
# this directly. REPO_ROOT + MCPCONFORMANCE_CLIENT_PATH resolve via
# _common.sh (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_CLIENT_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there:" \
    "  git clone https://github.com/modelcontextprotocol/conformance.git \$MCPCONFORMANCE_CLIENT_PATH" \
    "Or set MCPCONFORMANCE_CLIENT_PATH=<path-to-clone>."

echo "Building testclient..."
(cd "${REPO_ROOT}/cmd/testclient" && go build -buildvcs=false -o "${REPO_ROOT}/bin/testclient" .) || exit 1

if [ ! -f "${MCPCONFORMANCE_CLIENT_PATH}/dist/index.js" ]; then
    echo "Building upstream conformance dist/ (npm install)..."
    (cd "${MCPCONFORMANCE_CLIENT_PATH}" && npm install --silent) || exit 1
fi

OUT=$(mktemp -d -t conf-client.XXXXXX)
(cd "${MCPCONFORMANCE_CLIENT_PATH}" && \
    node dist/index.js client \
        --command "${REPO_ROOT}/bin/testclient" \
        --suite all \
        --expected-failures "${REPO_ROOT}/conformance/baseline.yml" \
        -o "$OUT")
RC=$?
echo "testconf-client: upstream runner exit $RC (artifacts: $OUT)"
if [ $RC -ne 0 ]; then
    echo "A scenario outside conformance/baseline.yml failed, or a listed entry now passes (stale entry — remove it)."
    exit $RC
fi

# Depth guard: a passing scenario that emits fewer checks than the committed
# snapshot is the thin-shadow signature (a setup failure masking most of the
# scenario's check surface — see scripts/check_client_check_counts.py).
python3 "${REPO_ROOT}/scripts/check_client_check_counts.py" "$OUT"
exit $?
