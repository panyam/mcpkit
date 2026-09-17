#!/usr/bin/env bash
# iterm-logs.sh — open one iTerm2 window with three vertically-stacked
# panes tailing logs from the three docker compose stacks the demo uses:
#
#   pane 1 (top):    backends   (keycloak + postgres + redis)
#   pane 2 (middle): observability (LGTM)
#   pane 3 (bottom): events     (this leaf's compose)
#
# Same osascript pattern as iterm-6up.sh — single window, three horizontal
# splits, each running `docker compose -f <stack> logs -f`. Bails out
# cleanly on non-macOS / no-iTerm hosts so `make alllogs` is safe in CI.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "iterm-logs.sh: requires macOS (iTerm2)." >&2
  echo "  Linux fallback — run in two sibling terminals:" >&2
  echo "    cd examples/whole-enchilada/events && make stack-logs" >&2
  echo "    docker compose -f docker/observability/docker-compose.yml logs -f" >&2
  exit 1
fi
if ! command -v osascript >/dev/null 2>&1; then
  echo "iterm-logs.sh: osascript not found — iTerm2 automation needs the macOS scripting bridge." >&2
  exit 1
fi
if [ ! -d /Applications/iTerm.app ] && [ ! -d "$HOME/Applications/iTerm.app" ]; then
  echo "iterm-logs.sh: iTerm.app not found in /Applications or ~/Applications. Install from https://iterm2.com" >&2
  exit 1
fi

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OBS_COMPOSE="$DEMO_DIR/../../../docker/observability/docker-compose.yml"
# The events stack owns postgres/redis/keycloak since the compose merge, so
# their logs come from the events project rather than docker/backends.
STACK_COMPOSE="docker compose -f events-stack.yaml -f compose.dev.yaml"

# printf %q quotes the path so a directory with spaces still works
# when the AppleScript pipes the string into the iTerm session as if
# typed.
CMD_BACKENDS=$(printf 'cd %q && %s logs -f postgres redis keycloak' "$DEMO_DIR" "$STACK_COMPOSE")
CMD_OBS=$(printf      'cd %q && docker compose -f %q logs -f' "$DEMO_DIR" "$OBS_COMPOSE")
CMD_EVENTS=$(printf   'cd %q && %s logs -f nginx event-server-1 event-server-2 event-server-3' "$DEMO_DIR" "$STACK_COMPOSE")

# Split semantics in iTerm2 AppleScript:
#   - "split horizontally" → horizontal divider, new pane BELOW
# Build top → middle → bottom by repeatedly horizontal-splitting the
# previous pane.
osascript <<APPLESCRIPT
tell application "iTerm"
    activate
    create window with default profile
    tell current window
        set backendsPane to current session
        tell backendsPane
            write text "$CMD_BACKENDS"
            set obsPane to (split horizontally with default profile)
        end tell
        tell obsPane
            write text "$CMD_OBS"
            set eventsPane to (split horizontally with default profile)
        end tell
        tell eventsPane to write text "$CMD_EVENTS"
    end tell
end tell
APPLESCRIPT
