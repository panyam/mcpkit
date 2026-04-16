ADDR ?= http://localhost:8080
MCP  := $(ADDR)/mcp

# ── Build & Run ──────────────────────────────────────────────────────
# Usage: make run                                — test mode (no Telegram)
#        TELEGRAM_MCPKIT_BOTTOKEN=... make run   — with Telegram bot

run:
	@if [ -n "$(TELEGRAM_MCPKIT_BOTTOKEN)" ]; then \
	  echo "Starting with Telegram bot..."; \
	  go run . -addr :8080 -token "$(TELEGRAM_MCPKIT_BOTTOKEN)"; \
	else \
	  echo "Starting in test mode (no Telegram token)..."; \
	  go run . -addr :8080; \
	fi

# ── Tests ────────────────────────────────────────────────────────────

test:
	go test ./... -v -count=1

test-race:
	go test ./... -race -count=1

# ── All-in-one diagnostic (requires server running on ADDR) ──────────
# Initializes a session, sends initialized notification, then exercises
# all tools, resources, and custom event methods.
#
# Streamable HTTP returns SSE (text/event-stream) — we use -N for no-buffer
# and extract the "data: " line to get the JSON payload.
#
# Usage: make diag
#        ADDR=http://host:9090 make diag
# Inject a test message directly (no Telegram needed)
# Usage: make inject TEXT="hello world"
#        make inject TEXT="test" SENDER="alice" CHAT_ID=123
inject:
	@curl -s -X POST $(ADDR)/inject \
	  -H 'Content-Type: application/json' \
	  -d '{"chat_id": $(or $(CHAT_ID),100), "sender": "$(or $(SENDER),test-user)", "text": "$(or $(TEXT),hello from make inject)"}' | jq .

diag:
	@./diag.sh $(MCP)

.PHONY: run test test-race inject diag
