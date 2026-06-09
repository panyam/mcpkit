#!/usr/bin/env bash
# iterm-6up.sh — open iTerm2 with a 2×3 grid of subscriber sessions
# against the running whole-enchilada stack.
#
#   row 1 (top):    poller  A | poller  B | poller  C
#   row 2 (bottom): webhook A | webhook B | webhook C
#
# Each pane runs `make <verb> TENANT=<X> USERNAME=user<x>1 PASSWORD=user<x>1`,
# so the binaries do their own ROPC login. Six fresh Keycloak sessions
# total — 1 per (user, client) pair — show up in the realm Sessions UI
# the moment they connect.
#
# Requires macOS + iTerm2 (the one that ships from iterm2.com, not the
# default Terminal.app). Bails out cleanly on other platforms.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "iterm-6up.sh: requires macOS (iTerm2)." >&2
  exit 1
fi
if ! command -v osascript >/dev/null 2>&1; then
  echo "iterm-6up.sh: osascript not found — iTerm2 automation needs the macOS scripting bridge." >&2
  exit 1
fi
if [ ! -d /Applications/iTerm.app ] && [ ! -d "$HOME/Applications/iTerm.app" ]; then
  echo "iterm-6up.sh: iTerm.app not found in /Applications or ~/Applications. Install from https://iterm2.com" >&2
  exit 1
fi

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Build one `cd && make <verb> ...` command per pane. printf %q quotes
# the path so a directory with spaces still works when the AppleScript
# pipes the string into the iTerm2 session as if typed.
cmd_for() {
  # ${tenant,,} is bash 4+; macOS ships bash 3.2, so route through tr.
  local tenant=$1 verb=$2 lower user
  lower=$(printf '%s' "$tenant" | tr '[:upper:]' '[:lower:]')
  user="user${lower}1"
  printf "cd %q && make %s TENANT=%s USERNAME=%s PASSWORD=%s" \
    "$DEMO_DIR" "$verb" "$tenant" "$user" "$user"
}

POLL_A=$(cmd_for A poller)
POLL_B=$(cmd_for B poller)
POLL_C=$(cmd_for C poller)
WEB_A=$(cmd_for A webhook)
WEB_B=$(cmd_for B webhook)
WEB_C=$(cmd_for C webhook)

# AppleScript split semantics (iTerm2):
#   - "split vertically"   → vertical divider, new pane to the RIGHT
#   - "split horizontally" → horizontal divider, new pane BELOW
# Order: build the top row with two vertical splits (A → A|B → A|B|C),
# then drop a horizontal divider on each top column to add its
# webhook pane below. Result is a clean 2×3 grid.
osascript <<APPLESCRIPT
tell application "iTerm"
    activate
    create window with default profile
    tell current window
        set pollA to current session
        tell pollA
            write text "$POLL_A"
            set pollB to (split vertically with default profile)
        end tell
        tell pollB
            write text "$POLL_B"
            set pollC to (split vertically with default profile)
        end tell
        tell pollC to write text "$POLL_C"
        tell pollA
            set webA to (split horizontally with default profile)
        end tell
        tell webA to write text "$WEB_A"
        tell pollB
            set webB to (split horizontally with default profile)
        end tell
        tell webB to write text "$WEB_B"
        tell pollC
            set webC to (split horizontally with default profile)
        end tell
        tell webC to write text "$WEB_C"
    end tell
end tell
APPLESCRIPT
