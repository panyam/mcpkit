#!/usr/bin/env bash
# Server-side auth conformance — fork-based scenarios (RFC 9728 PRM + RFC 8414
# AS metadata + RFC 6750 Bearer enforcement).
#
# Runner-agnostic: the Makefile + justfile `testconf-auth-server` recipes call
# this directly. REPO_ROOT + MCPCONFORMANCE_AUTH_PATH resolve via _common.sh
# (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_AUTH_PATH \
    "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_AUTH_PATH=<path-to-clone>." \
    "Default expects the 'pending' branch checked out at ../conf-pending."
(cd "${REPO_ROOT}/examples/auth" && go build -o auth-demo .)
(cd "${MCPCONFORMANCE_AUTH_PATH}" && npm install --silent)
# Manual spawn: Phase 2 + 2.5's token-needing checks pull four pre-
# minted tokens from the fixture's bootstrap (one valid, three
# deliberately-bad-claim variants), so we spawn here (instead of
# letting vitest auto-spawn) and curl /demo/bootstrap before
# invoking the scenarios.
"${REPO_ROOT}/examples/auth/auth-demo" --serve --addr=:18098 > /tmp/auth-demo.log 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf http://localhost:18098/.well-known/oauth-protected-resource > /dev/null 2>&1 && break
    sleep 0.5
done
BOOT=$(curl -sf http://localhost:18098/demo/bootstrap)
TOK_VALID=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_read"])' 2>/dev/null)
TOK_RW=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_read_write"])' 2>/dev/null)
TOK_FULL=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_all"])' 2>/dev/null)
TOK_EXPIRED=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_expired"])' 2>/dev/null)
TOK_WRONG_AUD=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_wrong_audience"])' 2>/dev/null)
TOK_WRONG_ISS=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_wrong_issuer"])' 2>/dev/null)
TOK_UPSTREAM=$(echo "$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_upstream_assertion"])' 2>/dev/null)
AS_TOKEN_ENDPOINT=$(curl -sf http://localhost:18098/.well-known/oauth-authorization-server | python3 -c 'import json,sys;print(json.load(sys.stdin)["token_endpoint"])' 2>/dev/null)
(cd "${MCPCONFORMANCE_AUTH_PATH}" && \
    AUTH_SERVER_URL=http://localhost:18098/mcp \
    AUTH_VALID_TOKEN="$TOK_VALID" \
    AUTH_READWRITE_TOKEN="$TOK_RW" \
    AUTH_FULL_TOKEN="$TOK_FULL" \
    AUTH_EXPIRED_TOKEN="$TOK_EXPIRED" \
    AUTH_WRONG_AUDIENCE_TOKEN="$TOK_WRONG_AUD" \
    AUTH_WRONG_ISSUER_TOKEN="$TOK_WRONG_ISS" \
    AUTH_SUBJECT_ASSERTION_TOKEN="$TOK_UPSTREAM" \
    AUTH_AS_TOKEN_ENDPOINT="$AS_TOKEN_ENDPOINT" \
    npx vitest run src/scenarios/server/auth/auth.test.ts)
RC=$?
kill $PID 2>/dev/null
wait $PID 2>/dev/null
exit $RC
