#!/usr/bin/env bash
# Smoke test for the examples/common --wire selection (issue 824).
#
# For each --wire-enabled example, boots it twice and asserts the wire
# selection actually reached the transport:
#   --wire=stateless  -> legacy `initialize` POST returns HTTP 404
#                        (initialize is a removed method on the SEP-2575
#                        stateless wire; the dispatcher 404s it)
#   default (no flag) -> legacy `initialize` POST returns HTTP 200
#                        (ModeDual still serves the legacy handshake)
#
# This guards the mechanical sweep against regression: a broken Wire
# wiring would leave both modes at 200 (flag ignored) or both at 404.
#
# Examples needing external infra (Keycloak) are listed in SKIP with a
# reason and printed, never silently dropped — their --wire wiring is
# covered by `go build` in CI, just not booted here.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
PORT=18300
PASS=0
FAIL=0
# When set, the probe uses this token instead of grepping the server log
# (for examples whose token comes from an external AS, e.g. Keycloak).
EXPLICIT_TOKEN=""

# Track the currently-booted example so an interrupt (Ctrl-C / SIGTERM)
# doesn't orphan a server holding a port. Cleanup runs on normal exit
# AND on signal.
CHILD_PID=""
cleanup() {
	[ -n "$CHILD_PID" ] && kill "$CHILD_PID" 2>/dev/null
	[ -n "$CHILD_PID" ] && wait "$CHILD_PID" 2>/dev/null
	rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}'

# check <name> <dir> <build-pkg> <serve|direct> [extra args...]
#   serve  -> invoke with `--serve --addr=:PORT` (dual-mode dispatch examples)
#   direct -> invoke with `-addr=:PORT` (always-serve examples, e.g. auth subs)
check() {
	local name="$1" dir="$2" pkg="$3" style="$4"
	shift 4
	local extra=("$@")
	local bin="$TMP/$name"

	if ! (cd "$ROOT/$dir" && go build -o "$bin" "$pkg") 2>"$TMP/$name.build"; then
		echo "BUILD FAIL $name"
		sed 's/^/    /' "$TMP/$name.build"
		FAIL=$((FAIL + 1))
		return
	fi

	local mode want
	for mode in stateless default; do
		PORT=$((PORT + 1))
		local addr=":$PORT"
		local args=()
		[ "$style" = serve ] && args+=("--serve")
		args+=("--addr=$addr" ${extra[@]+"${extra[@]}"})
		[ "$mode" = stateless ] && args+=("--wire=stateless")

		"$bin" "${args[@]}" >"$TMP/$name.$mode.log" 2>&1 &
		local pid=$!
		CHILD_PID=$pid

		# Readiness probe hits "/" (fast 404), NOT "/mcp" — a GET to /mcp
		# opens a long-lived SSE stream that would hang the probe.
		local up=""
		for _ in $(seq 1 60); do
			if curl -s -m 1 -o /dev/null "http://localhost$addr/" 2>/dev/null; then up=1; break; fi
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done

		# Auth examples gate `initialize` behind a bearer token, so an
		# unauthenticated probe gets 401 on BOTH wires and can't observe
		# the flip. They print a token at startup — grab it (a "Bearer
		# <tok>" line, else a bare JWT) and authenticate the probe so the
		# wire flip becomes visible (200 dual / 404 stateless).
		local auth=()
		local tok="$EXPLICIT_TOKEN"
		if [ -z "$tok" ]; then
			# Prefer a real JWT (jwt/scopes/session-binding mint these).
			# Fall back to a Bearer token whose char class excludes '<'
			# '>' so the help line "...Authorization: Bearer <token>):"
			# some examples print doesn't get grabbed as the literal
			# placeholder (bearer example prints a static token).
			tok=$(grep -oE 'eyJ[A-Za-z0-9_.-]+' "$TMP/$name.$mode.log" 2>/dev/null | head -1)
			[ -z "$tok" ] && tok=$(grep -oE 'Bearer [A-Za-z0-9._-]+' "$TMP/$name.$mode.log" 2>/dev/null | head -1 | awk '{print $2}')
		fi
		[ -n "$tok" ] && auth=(-H "Authorization: Bearer $tok")

		local code="dead"
		if [ -n "$up" ]; then
			code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' -X POST "http://localhost$addr/mcp" \
				-H 'content-type: application/json' -H 'Mcp-Session-Id: smoke' \
				${auth[@]+"${auth[@]}"} -d "$INIT")
		fi
		kill "$pid" 2>/dev/null
		wait "$pid" 2>/dev/null
		CHILD_PID=""

		[ "$mode" = stateless ] && want=404 || want=200
		if [ "$code" = "$want" ]; then
			echo "ok   $name [$mode] initialize=$code"
			PASS=$((PASS + 1))
		else
			echo "FAIL $name [$mode] initialize=$code want=$want  (log: $TMP/$name.$mode.log)"
			sed 's/^/    /' "$TMP/$name.$mode.log" | tail -4
			FAIL=$((FAIL + 1))
		fi
	done
}

echo "=== wire-selection smoke (issue 824) ==="

# Plain examples — unauthenticated `initialize` reaches the dispatcher.
check tasks         examples/tasks               .                  serve
check tasks-v2      examples/tasks-v2            .                  serve
check mrtr          examples/mrtr                .                  serve
check file-inputs   examples/file-inputs         .                  serve
check list-ttl      examples/list-ttl            .                  serve
check elicitation   examples/elicitation         .                  serve
check skills        examples/skills              .                  serve "--skills=$ROOT/examples/skills/skills"
check kitchen-sink  examples/events/kitchen-sink .                  serve

# Auth examples — the probe picks up the token each prints at startup.
# auth-demo / public-discovery / unified allow an anonymous initialize;
# jwt / bearer / scopes / session-binding gate it (token-authenticated).
check auth-demo     examples/auth         .                  serve
check auth-pubdisc  examples/auth         ./public-discovery direct
check auth-unified  examples/auth         ./unified          direct
check auth-jwt      examples/auth         ./jwt              direct
check auth-bearer   examples/auth         ./bearer           direct
check auth-scopes   examples/auth         ./scopes           direct
check auth-sessbind examples/auth         ./session-binding  direct

# step-up-keycloak needs a live Keycloak (it discovers the realm at boot).
# Run it ONLY when Keycloak is already up — never bring it up here. Mint a
# client-credentials token from the test realm to authenticate the probe.
KC_PORT="${KC_PORT:-8180}"
KC_REALM="${KC_REALM:-mcpkit-test}"
KC_REALM_URL="http://localhost:$KC_PORT/realms/$KC_REALM"
if curl -sf -m 2 -o /dev/null "$KC_REALM_URL" 2>/dev/null; then
	EXPLICIT_TOKEN=$(curl -s -m 5 -X POST "$KC_REALM_URL/protocol/openid-connect/token" \
		-d grant_type=client_credentials \
		-d client_id=mcp-confidential \
		-d client_secret=mcp-test-secret-for-confidential 2>/dev/null \
		| grep -oE '"access_token":"[^"]+"' | sed 's/.*:"//; s/"$//')
	if [ -n "$EXPLICIT_TOKEN" ]; then
		echo "(keycloak up on :$KC_PORT — minted client-credentials token)"
		check auth-step-up-kc examples/auth ./step-up-keycloak direct
	else
		echo "SKIP auth-step-up-kc — keycloak up but client-credentials token fetch failed"
	fi
	EXPLICIT_TOKEN=""
else
	echo "SKIP auth-step-up-kc — Keycloak not running on :$KC_PORT (start it with: just upkcl)"
fi

echo
echo "SKIPPED (--wire wiring covered by 'go build'; not booted here):"
echo "  fine-grained-auth"
echo "    — initialize is auth-gated and serve() prints no static token (a real"
echo "      token needs an OAuth flow against the AS), so the probe can't authenticate."

echo
echo "=== wire smoke: $PASS pass / $FAIL fail ==="
[ "$FAIL" -eq 0 ]
