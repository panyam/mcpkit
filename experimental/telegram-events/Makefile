ADDR ?= http://localhost:8080
MCP  := $(ADDR)/mcp
EVENT := telegram.message
RESOURCE_URI := telegram://messages/recent
EVENTS_CLIENT := python3 ../ext/events/events_client.py --mcp $(MCP) --event $(EVENT) --resource-uri $(RESOURCE_URI)

# ── Build & Run ──────────────────────────────────────────────────────
# Usage: make run                                — test mode (no Telegram)
#        TELEGRAM_BOT_TOKEN=... make run   — with Telegram bot

run:
	@if [ -n "$(TELEGRAM_BOT_TOKEN)" ]; then \
	  echo "Starting with Telegram bot..."; \
	  go run . -addr :8080 -token "$(TELEGRAM_BOT_TOKEN)"; \
	else \
	  echo "Starting in test mode (no Telegram token)..."; \
	  go run . -addr :8080; \
	fi

# ── Tests ────────────────────────────────────────────────────────────

test:
	go test ./... -v -count=1

test-race:
	go test ./... -race -count=1

# ── Inject + Client Modes ────────────────────────────────────────────

inject:
	@curl -s -X POST $(ADDR)/inject \
	  -H 'Content-Type: application/json' \
	  -d '{"chat_id": $(or $(CHAT_ID),100), "sender": "$(or $(SENDER),test-user)", "text": "$(or $(TEXT),hello from make inject)"}' | jq .

list:
	@$(EVENTS_CLIENT) list

listen:
	@$(EVENTS_CLIENT) listen

webhook:
	@$(EVENTS_CLIENT) webhook --port $(or $(HOOK_PORT),9999)

poll:
	@$(EVENTS_CLIENT) poll --interval $(or $(INTERVAL),5)

.PHONY: run test test-race inject list listen webhook poll
