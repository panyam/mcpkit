#!/usr/bin/env bash
# Dual-mode behavioral verification for examples (issue 478).
#
# Where smoke-wire.sh proves the wire *selects* (initialize 200/404),
# this drives each auto-drivable example's full walkthrough end-to-end
# against a server on each wire and asserts the flow actually WORKS —
# same observable behavior on the legacy session wire and the SEP-2575
# stateless wire.
#
# Mechanism: boot `<example> --serve --wire=<w>`, run the walkthrough
# `<example> --non-interactive --url <addr> --wire=<w>`, and assert the
# run produced NO failure markers. demokit's non-interactive renderer
# always exits 0 (even on a step failure), so the signal is the output:
# the common helpers print an indented "ERROR:" / "UNEXPECTED:" line on a
# failed step, and a clean run ends with "=== Done ===".
#
# NOTE: common.ServerURL() only matches the space form `--url X` (not
# `--url=X`), so the walkthrough invocation uses the space form.
#
# Only auto-drivable examples run here. Interactive (elicitation consent,
# fine-grained-auth/Keycloak), UI (apps/*), and infra (events/*, host/*)
# examples are audited in examples/DUAL_MODE_AUDIT.md instead.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
PORT=18400
PASS=0
FAIL=0

CHILD_PID=""
cleanup() {
	[ -n "$CHILD_PID" ] && kill "$CHILD_PID" 2>/dev/null
	[ -n "$CHILD_PID" ] && wait "$CHILD_PID" 2>/dev/null
	rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

# verify <name> <dir> [server extra args...]
# Builds once, then runs the walkthrough on the legacy and stateless wires.
verify() {
	local name="$1" dir="$2"
	shift 2
	local extra=("$@")
	local bin="$TMP/$name"

	if ! (cd "$ROOT/$dir" && go build -o "$bin" .) 2>"$TMP/$name.build"; then
		echo "BUILD FAIL $name"
		sed 's/^/    /' "$TMP/$name.build"
		FAIL=$((FAIL + 1))
		return
	fi

	local wire
	for wire in legacy stateless; do
		PORT=$((PORT + 1))
		local addr=":$PORT"

		"$bin" --serve --addr="$addr" ${extra[@]+"${extra[@]}"} --wire="$wire" \
			>"$TMP/$name.$wire.srv.log" 2>&1 &
		local pid=$!
		CHILD_PID=$pid

		local up=""
		for _ in $(seq 1 60); do
			if curl -s -m 1 -o /dev/null "http://localhost$addr/" 2>/dev/null; then up=1; break; fi
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done

		if [ -z "$up" ]; then
			echo "FAIL $name [$wire] — server never came up (log: $TMP/$name.$wire.srv.log)"
			sed 's/^/    /' "$TMP/$name.$wire.srv.log" | tail -4
			kill "$pid" 2>/dev/null
			wait "$pid" 2>/dev/null
			CHILD_PID=""
			FAIL=$((FAIL + 1))
			continue
		fi

		local out="$TMP/$name.$wire.demo.log"
		(cd "$ROOT/$dir" && "$bin" --non-interactive --url "http://localhost$addr" --wire="$wire") \
			>"$out" 2>&1

		kill "$pid" 2>/dev/null
		wait "$pid" 2>/dev/null
		CHILD_PID=""

		# A clean run has no indented failure marker; the common helpers
		# print "    ERROR:" / "    UNEXPECTED:" on a failed step.
		local bad
		bad=$(grep -cE '^[[:space:]]*(ERROR|UNEXPECTED|panic:)' "$out" 2>/dev/null || true)
		if [ "$bad" = "0" ]; then
			echo "ok   $name [$wire] — walkthrough clean"
			PASS=$((PASS + 1))
		else
			echo "FAIL $name [$wire] — $bad failure marker(s) (log: $out)"
			grep -nE '^[[:space:]]*(ERROR|UNEXPECTED|panic:)' "$out" | head -4 | sed 's/^/    /'
			FAIL=$((FAIL + 1))
		fi
	done
}

echo "=== dual-mode walkthrough verification (issue 478) ==="

# The auto-drivable, --wire-honoring examples that are clean on BOTH wires.
# Each runs its walkthrough non-interactively against a legacy server and a
# stateless server and must produce no failure markers.
verify tasks-v2    examples/tasks-v2
verify mrtr        examples/mrtr
verify file-inputs examples/file-inputs
verify list-ttl    examples/list-ttl

echo
echo "NOT auto-verified here (see examples/DUAL_MODE_AUDIT.md for the full matrix):"
echo "  tasks (v1):       deprecated, legacy-only — not targeted at the stateless wire."
echo "  skills:           drives its client wire via its own interactive demokit.Choice,"
echo "                    so --wire can't steer it; verified manually (Adaptive vs stateless)."
echo "  interactive:      elicitation (consent URL), fine-grained-auth (Keycloak browser flow)."
echo "  UI / push:        apps/* — AppHost-mediated, use ctx.Elicit/Sample (legacy; migration tracked)."
echo "  infra / not-wired: events/*, whole-enchilada, host/* (not --wire-threaded)."

echo
echo "=== dual-mode verify: $PASS pass / $FAIL fail ==="
[ "$FAIL" -eq 0 ]
