ADDR ?= http://localhost:8080
MCP  := $(ADDR)/mcp
EVENT := discord.message
RESOURCE_URI := discord://messages/recent
EVENTS_CLIENT := python3 ../../../experimental/ext/events/clients/python/events_client.py --mcp $(MCP) --event $(EVENT) --resource-uri $(RESOURCE_URI)

# ── Build & Run ──────────────────────────────────────────────────────
# Usage: make run                              — test mode (no Discord)
#        DISCORD_BOT_TOKEN=... make run  — with Discord bot

run: serve  ## Alias for serve

serve: ## Start the discord-events server standalone (also works with MCPJam, VS Code, Claude Desktop)
	@if [ -n "$(DISCORD_BOT_TOKEN)" ]; then \
	  echo "Starting with Discord bot..."; \
	  go run . --serve -addr :8080 -token "$(DISCORD_BOT_TOKEN)"; \
	else \
	  echo "Starting in test mode (no Discord token)..."; \
	  go run . --serve -addr :8080; \
	fi

demo: ## Run the demokit walkthrough (interactive, TUI). Server must be up via `make serve` in another terminal.
	go run . --tui

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc=md > WALKTHROUGH.md

build: ## Build the binary
	go build -o discord-events-demo .

# ── Tests ────────────────────────────────────────────────────────────

test:
	go test ./... -v -count=1

test-race:
	go test ./... -race -count=1

# Drives the Python WebhookSubscription auto-refresh helper against a server
# running with a short TTL. Exits 0 on success, non-zero on failure.
#
# POSIX-only (uses lsof, xargs -r, bash trap, kill -9). On Windows, run the
# server and the Python driver manually:
#   1. Manually kill anything bound to TTL_PORT / TTL_HOOK_PORT
#   2. go run . -addr :18080 -webhook-ttl 3s
#   3. python3 test_ttl.py --mcp http://localhost:18080/mcp \
#        --inject-url http://localhost:18080/inject --port 19999 --ttl 3 --duration 8
TTL_PORT       ?= 18080
TTL_HOOK_PORT  ?= 19999
TTL_SECONDS    ?= 3
TTL_DURATION   ?= 8

test-ttl:
	@echo "[test-ttl] killing any existing processes on :$(TTL_PORT) / :$(TTL_HOOK_PORT)..."
	@lsof -ti :$(TTL_PORT) 2>/dev/null | xargs -r kill -9 2>/dev/null || true
	@lsof -ti :$(TTL_HOOK_PORT) 2>/dev/null | xargs -r kill -9 2>/dev/null || true
	@sleep 0.3
	@echo "[test-ttl] starting server on :$(TTL_PORT) with -webhook-ttl $(TTL_SECONDS)s..."
	@bash -c '\
	  set -e; \
	  go build -o /tmp/discord-events-ttl-bin . ; \
	  /tmp/discord-events-ttl-bin --serve -addr :$(TTL_PORT) -webhook-ttl $(TTL_SECONDS)s > /tmp/discord-events-ttl.log 2>&1 & \
	  SERVER_PID=$$!; \
	  trap "kill -9 $$SERVER_PID 2>/dev/null || true; lsof -ti :$(TTL_PORT) 2>/dev/null | xargs -r kill -9 2>/dev/null || true; rm -f /tmp/discord-events-ttl-bin" EXIT; \
	  for i in 1 2 3 4 5 6 7 8 9 10; do \
	    if curl -sf -o /dev/null http://localhost:$(TTL_PORT)/mcp -X POST -H "Content-Type: application/json" -d "{}" 2>/dev/null || nc -z localhost $(TTL_PORT) 2>/dev/null; then \
	      break; \
	    fi; \
	    sleep 0.2; \
	  done; \
	  echo "[test-ttl] running driver (duration $(TTL_DURATION)s)..."; \
	  python3 test_ttl.py \
	    --mcp http://localhost:$(TTL_PORT)/mcp \
	    --inject-url http://localhost:$(TTL_PORT)/inject \
	    --port $(TTL_HOOK_PORT) \
	    --ttl $(TTL_SECONDS) \
	    --duration $(TTL_DURATION); \
	'

# ── Inject + Client Modes ────────────────────────────────────────────

inject:
	@curl -s -X POST $(ADDR)/inject \
	  -H 'Content-Type: application/json' \
	  -d '{"guild_id": "$(or $(GUILD),test-guild)", "channel_id": "$(or $(CHANNEL),test-channel)", "sender": "$(or $(SENDER),test-user)", "text": "$(or $(TEXT),hello from make inject)"}' | jq .

inject-typing:
	@curl -s -X POST '$(ADDR)/inject?event=discord.typing' \
	  -H 'Content-Type: application/json' \
	  -d '{"guild_id": "$(or $(GUILD),test-guild)", "channel_id": "$(or $(CHANNEL),test-channel)", "user": "$(or $(USER_NAME),test-user)"}' | jq .

# ζ-7.4: source-side health-signal injection. inject-error sends a
# transient upstream-failure signal (push subscribers see
# notifications/events/error; subscription stays open). inject-terminate
# is one-shot terminal (push subscribers see notifications/events/
# terminated and stream closes; webhook subscribers get a type:terminated
# control envelope). Both demonstrate the ε-2 dormant callbacks coming
# alive via the ζ-7 source-side API.
inject-error:
	@curl -s -X POST '$(ADDR)/inject?action=error&code=$(or $(CODE),-32603)&message=$(or $(MESSAGE),demo+upstream+failure)' | jq .

inject-terminate:
	@curl -s -X POST '$(ADDR)/inject?action=terminate&code=$(or $(CODE),-32012)&message=$(or $(MESSAGE),demo+terminated)' | jq .

list:
	@$(EVENTS_CLIENT) list

listen:
	@$(EVENTS_CLIENT) listen

webhook:
	@$(EVENTS_CLIENT) webhook --port $(or $(HOOK_PORT),9999)

poll:
	@$(EVENTS_CLIENT) poll --interval $(or $(INTERVAL),5)

.PHONY: run serve demo readme build test test-race test-ttl inject inject-typing inject-error inject-terminate list listen webhook poll
.DEFAULT_GOAL := demo
