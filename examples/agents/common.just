# Shared recipes for agent examples. Import from an example justfile:
#   import '../common.just'
#
# One config per example drives every surface: `chat` (agentchat CLI) and
# `web` (agentweb browser) both load CONFIG, and the deterministic `agent`
# scenario loads the same file in code (see each example's README for the
# split between JSON config and the in-code scripted StubProvider).
#
# `demo` resolves model/base-url from ../llm.json (or ../llm.local.json) when
# MODEL/BASE_URL are unset — see ../demo.sh. Keys are never in the file.

# Host config both surfaces load. Override in an example justfile if the file
# is not named config.json.
CONFIG := justfile_directory() / "config.json"

# Address the agentweb browser surface listens on.
ADDR := env_var_or_default("ADDR", ":8090")

# Run the deterministic scripted scenario (no LLM)
agent:
    go run .

# Run against a live model (MODEL=<name> BASE_URL=<url> override llm.json)
demo:
    @../demo.sh

# Chat with the CLI surface (agentchat) off CONFIG. Start the example's
# server first (just serve) so the configured tools are reachable.
chat:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "$(git rev-parse --show-toplevel)/cmd/agentchat"
    exec go run . --config "{{CONFIG}}"

# Serve the browser surface (agentweb) off CONFIG on ADDR. Start the example's
# server first (just serve); a down server does not block boot, its tools wire
# in once it is up.
web:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "$(git rev-parse --show-toplevel)/agent/web"
    exec go run ./cmd/agentweb --config "{{CONFIG}}" --addr "{{ADDR}}"

# Run the golden-transcript test
test:
    go test ./... -count=1
