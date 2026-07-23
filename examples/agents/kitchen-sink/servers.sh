#!/usr/bin/env bash
# Manage the kitchen-sink MCP servers INDEPENDENTLY of the agent.
#
# CONSTRAINT (root CONSTRAINTS.md): the agent is a pure MCP client — it connects
# to servers by URL and does not own their process lifecycle. Bringing servers
# up and down is this launcher's job, not the agent's. So these servers are
# started here, survive agentchat restarts, and are torn down explicitly.
#
# Usage:
#   servers.sh up      [name]   # build + start all servers detached (or just <name>)
#   servers.sh up-fg   [name]   # start + tail logs in the foreground (Ctrl+C stops them)
#   servers.sh down    [name]   # stop all (or just <name>)
#   servers.sh status  [name]   # probe ports; show up/down
#
# Each server is built to .servers/bin/<name> and run detached (the PID is the
# server itself — a `go run` wrapper would orphan its child on kill). PIDs and
# logs live under .servers/ (gitignored).
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
STATE="$DIR/.servers"
mkdir -p "$STATE/bin"

# The server table. bash 3.2 (macOS system bash) has no associative arrays, so
# a case function maps name -> "workdir|port|run-args". Keep in sync with the
# server entries in kitchen-sink.json.
ORDER="demo runbooks community events"
server_spec() {
	case "$1" in
	demo)      echo "$DIR/server|8788|" ;;
	runbooks)  echo "$ROOT/examples/skills-core|8789|--serve --addr=:8789" ;;
	community) echo "$ROOT/examples/skills|8790|--serve --addr=:8790" ;;
	events)    echo "$ROOT/examples/events/kitchen-sink|8791|--serve --addr=:8791" ;;
	*) return 1 ;;
	esac
}

port_up() { # port -> 0 if something is listening
	(exec 3<>"/dev/tcp/localhost/$1") 2>/dev/null && { exec 3>&-; return 0; }
	return 1
}

up_one() {
	local name="$1" spec dir port args
	spec="$(server_spec "$name")" || { echo "!! unknown server: $name" >&2; return 1; }
	IFS='|' read -r dir port args <<<"$spec"
	if port_up "$port"; then echo "  [up]   $name :$port (already running)"; return 0; fi
	local bin="$STATE/bin/$name" log="$STATE/$name.log" pidf="$STATE/$name.pid"
	echo "==> building $name"
	( cd "$dir" && go build -o "$bin" . ) || { echo "!! build $name failed" >&2; return 1; }
	echo "==> starting $name on :$port (log -> $log)"
	# Detach so it outlives this shell AND the agent. `exec nohup` collapses the
	# subshell -> nohup -> binary chain onto ONE pid, so $! is the server itself
	# (a plain `nohup bin &` would record the wrapper's pid and orphan the real
	# process on `down`). $args is unquoted so "--serve --addr=:PORT" word-splits
	# (empty for demo). nohup ignores SIGHUP so it survives the launching shell.
	( cd "$dir" && exec nohup "$bin" $args >"$log" 2>&1 ) &
	echo $! >"$pidf"
	local i
	for i in $(seq 1 60); do
		port_up "$port" && { echo "  [up]   $name :$port"; return 0; }
		sleep 0.5
	done
	echo "!! $name did not come up on :$port — see $log" >&2
	return 1
}

down_one() {
	# Split declarations: bash 3.2 + `set -u` errors if one `local` both declares
	# name and expands $name in the same statement.
	local name="$1"
	local pidf="$STATE/$name.pid"
	local pid=""
	if [ -f "$pidf" ]; then
		pid="$(cat "$pidf")"
		if kill "$pid" 2>/dev/null; then echo "  [down] $name (pid $pid)"; else echo "  [--]   $name (pid $pid not running)"; fi
		rm -f "$pidf"
	else
		echo "  [--]   $name (no pidfile; not started by this script)"
	fi
}

status_one() {
	local name="$1" spec dir port args
	spec="$(server_spec "$name")" || { echo "!! unknown server: $name" >&2; return 1; }
	IFS='|' read -r dir port args <<<"$spec"
	if port_up "$port"; then echo "  [up]   $name :$port"; else echo "  [down] $name :$port"; fi
}

# up_fg starts the servers (reusing the reliable detached start + pidfiles) and
# then tails all their logs in the FOREGROUND, so you can watch every server's
# output live in one terminal. Ctrl+C stops the tail AND brings the servers down
# — this window owns their lifetime, unlike the detached `up`. Run agentchat in a
# SEPARATE terminal: its inline TUI and these interleaved logs would fight for the
# same screen.
up_fg() {
	local n
	trap 'echo; echo "==> stopping servers (foreground window closed)"; for n in $names; do down_one "$n"; done; exit 0' INT TERM
	for n in $names; do up_one "$n" || true; done
	local logs=""
	for n in $names; do logs="$logs $STATE/$n.log"; done
	echo "==> tailing $(echo $names | wc -w | tr -d ' ') server log(s) — Ctrl+C stops them all"
	# -F follows across truncation/rotation and waits for a not-yet-created log
	# (a server still building); multiple files print a "==> name.log <==" header.
	# shellcheck disable=SC2086
	tail -n +1 -F $logs
}

cmd="${1:-status}"
target="${2:-}"
names="$ORDER"
[ -n "$target" ] && names="$target"

case "$cmd" in
up)     for n in $names; do up_one "$n"; done ;;
up-fg)  up_fg ;;
down)   for n in $names; do down_one "$n"; done ;;
status) echo "kitchen-sink MCP servers"; for n in $names; do status_one "$n"; done ;;
*) echo "usage: servers.sh <up|up-fg|down|status> [name]" >&2; exit 2 ;;
esac
