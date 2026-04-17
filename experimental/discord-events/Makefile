ADDR ?= http://localhost:8080
MCP  := $(ADDR)/mcp

# ── Build & Run ──────────────────────────────────────────────────────
# Usage: make run                              — test mode (no Discord)
#        DISCORD_BOT_TOKEN=... make run  — with Discord bot

run:
	@if [ -n "$(DISCORD_BOT_TOKEN)" ]; then \
	  echo "Starting with Discord bot..."; \
	  go run . -addr :8080 -token "$(DISCORD_BOT_TOKEN)"; \
	else \
	  echo "Starting in test mode (no Discord token)..."; \
	  go run . -addr :8080; \
	fi

# ── Tests ────────────────────────────────────────────────────────────

test:
	go test ./... -v -count=1

test-race:
	go test ./... -race -count=1

# ── Inject + Diagnostics ─────────────────────────────────────────────

inject:
	@curl -s -X POST $(ADDR)/inject \
	  -H 'Content-Type: application/json' \
	  -d '{"guild_id": "$(or $(GUILD),test-guild)", "channel_id": "$(or $(CHANNEL),test-channel)", "sender": "$(or $(SENDER),test-user)", "text": "$(or $(TEXT),hello from make inject)"}' | jq .

diag:
	@./diag.sh $(MCP)

.PHONY: run test test-race inject diag
