#!/usr/bin/env bash
# Resolve a model / base-url / api-key-env from llm(.local).json when they are
# not passed via the MODEL / BASE_URL env vars, then run the example
# (`go run .`) in the current directory with those flags.
#
# SECURITY: this file (and the committed llm.json) NEVER holds a key —
# apiKeyEnv names an env var that the provider reads at runtime. Nothing here
# puts a literal key on the command line or in git.
#
# Precedence: MODEL/BASE_URL env  >  llm.local.json (gitignored)  >  llm.json.
# Set DEMO_PRINT=1 to print the resolved command instead of running it.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CFG="$DIR/llm.local.json"
[ -f "$CFG" ] || CFG="$DIR/llm.json"

model="${MODEL:-}"
base="${BASE_URL:-}"
keyenv=""

if [ -f "$CFG" ] && command -v jq >/dev/null 2>&1; then
  active="$(jq -r '.connections.active // empty' "$CFG")"
  if [ -n "$active" ]; then
    conn="$(jq -c --arg a "$active" '.connections.connections[$a] // {}' "$CFG")"
    [ -n "$model" ] || model="$(jq -r '.model // empty' <<<"$conn")"
    keyenv="$(jq -r '.apiKeyEnv // empty' <<<"$conn")"
    if [ -z "$base" ]; then
      base="$(jq -r '.baseURL // empty' <<<"$conn")"
      if [ -z "$base" ]; then
        case "$(jq -r '.type // empty' <<<"$conn")" in
          lmstudio)   base="http://localhost:1234/v1" ;;
          ollama)     base="http://localhost:11434/v1" ;;
          openai)     base="https://api.openai.com/v1" ;;
          openrouter) base="https://openrouter.ai/api/v1" ;;
        esac
      fi
    fi
  fi
fi

if [ -z "$model" ]; then
  echo "demo: no model — set MODEL=<name> or an active connection in llm.json" >&2
  exit 1
fi

args=(--model "$model")
[ -n "$base" ] && args+=(--base-url "$base")
[ -n "$keyenv" ] && args+=(--api-key-env "$keyenv")

if [ "${DEMO_PRINT:-}" = "1" ]; then
  echo "go run . ${args[*]}"
  exit 0
fi
exec go run . "${args[@]}"
