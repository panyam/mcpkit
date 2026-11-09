#!/usr/bin/env bash
# The exercise for mission #1683: a user asks an agent from inside a custom
# web app, and the agent drives the page. The app is examples/rooms/lanternfall.
#
#   make exercise-aipage
#
# Three checks:
#   1. Scripted: an MCP client attaches, calls the page's tools, and hears the
#      player's events while Chromium plays the player.
#   2. Custom agent: a LangGraph.js game master with its own system prompt
#      answers a question typed into the page. Needs ANTHROPIC_API_KEY, and
#      says SKIPPED without it.
#   3. By hand: the steps to play with Claude Code are printed at the end.
#
# Exits 1 if a check failed.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/examples/rooms/lanternfall"
fail=0

step() { printf '\n== %s\n' "$1"; }

step "Go tests of the example server"
(cd "$APP" && go test ./... -count=1) || fail=1

step "The page builds, and the committed static/app.js is current"
bash "$APP/scripts/check-web.sh" || fail=1

step "Scripted check, and the custom agent when a key is set"
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "custom agent: SKIPPED (ANTHROPIC_API_KEY is not set)"
fi
bash "$APP/scripts/test-e2e.sh" || fail=1

step "By hand, with Claude Code"
cat <<STEPS
1. Start the game:              (cd examples/rooms/lanternfall && go run .)
2. Open the page as alice:       http://localhost:8080/signin?as=alice&game=demo
3. Install the channel from this tree (it picks up declared events):
     (cd experimental/integrations/roomsevents && go install ./cmd/rooms-channel)
4. Add to .mcp.json:
     {"mcpServers": {"rooms": {"command": "rooms-channel",
       "args": ["-bridge", "http://localhost:8080/mcp"],
       "env": {"ROOMS_BRIDGE_TOKEN": "player:alice"}}}}
5. Start Claude Code:            claude --dangerously-load-development-channels server:rooms
   Tell it: "Be the game master of lanternfall:demo."
6. On the page, turn on "Let my game master drive this tab", walk around, then ask something in the box.
   Claude Code gets your question as a room.ask channel message and answers on the map.
Or a custom agent instead of Claude Code:  (cd examples/rooms/lanternfall && make gm)
STEPS

if [ "$fail" -ne 0 ]; then
  printf '\nexercise-aipage: FAILED\n'
  exit 1
fi
printf '\nexercise-aipage: passed\n'
