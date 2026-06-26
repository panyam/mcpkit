#!/usr/bin/env bash
# iterm-6up.sh — open iTerm2 with a 3×2 grid of subscriber sessions
# against the running whole-enchilada stack.
#
#   col 1 (left):  streamer A | webhook A   (row 1)
#                  streamer B | webhook B   (row 2)
#                  streamer C | webhook C   (row 3)
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
#
# User identities match the walkthrough's Phase 1 matrix: alice/bob/carol
# stream, anand/bhavna/chandan webhook. Six distinct users → six Keycloak
# sessions in the realm Sessions UI, which is what the demo narrates.
# The Makefile defaults PASSWORD to USERNAME when omitted, so we only
# pass USERNAME.
cmd_for() {
  local tenant=$1 verb=$2 user=$3
  printf "cd %q && make %s TENANT=%s USERNAME=%s" \
    "$DEMO_DIR" "$verb" "$tenant" "$user"
}

STREAM_A=$(cmd_for A streamer alice)
STREAM_B=$(cmd_for B streamer bob)
STREAM_C=$(cmd_for C streamer carol)
WEBHOOK_A=$(cmd_for A webhook anand)
WEBHOOK_B=$(cmd_for B webhook bhavna)
WEBHOOK_C=$(cmd_for C webhook chandan)

# AppleScript split semantics (iTerm2):
#   - "split vertically"   → vertical divider, new pane to the RIGHT
#   - "split horizontally" → horizontal divider, new pane BELOW
# Order: build the left column with two horizontal splits
# (A → A/B → A/B/C, top to bottom), then drop a vertical divider on
# each left pane to add its webhook pane to the right. Result is a
# clean 3×2 grid.
osascript <<APPLESCRIPT
tell application "iTerm"
    activate
    create window with default profile
    tell current window
        set pollA to current session
        tell pollA
            write text "$STREAM_A"
            set pollB to (split horizontally with default profile)
        end tell
        tell pollB
            write text "$STREAM_B"
            set pollC to (split horizontally with default profile)
        end tell
        tell pollC to write text "$STREAM_C"
        tell pollA
            set webA to (split vertically with default profile)
        end tell
        tell webA to write text "$WEBHOOK_A"
        tell pollB
            set webB to (split vertically with default profile)
        end tell
        tell webB to write text "$WEBHOOK_B"
        tell pollC
            set webC to (split vertically with default profile)
        end tell
        tell webC to write text "$WEBHOOK_C"
    end tell
end tell
APPLESCRIPT
