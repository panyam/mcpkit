# kitchen-sink — every agent feature wired at once (Makefile mirror of the
# justfile). Quick start:
#   make allup     # postgres+pgvector and observability stacks
#   make run       # preflight, boot the demo server, launch agentchat
#   make alldown   # tear the stacks back down
#
# Every store/endpoint is a variable: override on the CLI or via env,
#   e.g.  make run SESSION=demo2 EMBED_DIM=1536
# The chat MODEL comes from kitchen-sink.json's connections block; only the
# embedder is passed by flag.

# --- chat + backends -----------------------------------------------------
# ACTIVE overrides the config's active chat connection (e.g. anthropic-opus);
# empty keeps kitchen-sink.json's default (local).
ACTIVE        ?=
SESSION_STORE ?= postgres://postgres:postgres@localhost:5432/agent
SESSION       ?= kitchen-sink
# --- offloading ----------------------------------------------------------
OFFLOAD_THRESHOLD ?= 1024
# --- semantic pgvector memory --------------------------------------------
# Default: embedder comes from kitchen-sink.json's `embedder` role (a cloud
# embedding connection — set that provider's API key). Set EMBED_MODEL to
# override with an explicit endpoint instead.
EMBED_MODEL       ?=
EMBED_URL         ?= http://localhost:1234/v1
EMBED_DIM         ?= 768
EMBED_API_KEY_ENV ?=
# --- compaction ----------------------------------------------------------
COMPACT_TOKENS ?= 8000
# --- observability -------------------------------------------------------
EXPORTER      ?= otlp
OTLP_ENDPOINT ?= localhost:4317
# --- misc ----------------------------------------------------------------
UI      ?= tui
PG_HOST ?= localhost
PG_PORT ?= 5432
PG_DB   ?= agent
PG_USER ?= postgres

export ACTIVE SESSION_STORE SESSION OFFLOAD_THRESHOLD EMBED_MODEL EMBED_URL EMBED_DIM \
       EMBED_API_KEY_ENV COMPACT_TOKENS EXPORTER OTLP_ENDPOINT UI \
       PG_HOST PG_PORT PG_DB PG_USER

.PHONY: check preflight run demo note allup alldown up obs-up psql mem server build

check: ## Probe every backend; print bring-up instructions for what's missing.
	@bash preflight.sh
preflight: check

run: ## Preflight, boot the demo server, launch agentchat with everything wired.
	@bash run.sh
demo: run

note: ## Same as run, but the alt-screen notebook UI (scrollable, foldable cells).
	@UI=notebook bash run.sh

allup: ## Bring up ALL stacks kitchen-sink can use (backends + observability).
	cd ../../../docker/backends && $(MAKE) up
	cd ../../../docker/observability && $(MAKE) up
	@echo ""
	@echo "  Stacks up. The embedder is external (LM Studio / Ollama) — see 'make check'."

alldown: ## Tear down ALL stacks brought up by allup.
	cd ../../../docker/observability && $(MAKE) down
	cd ../../../docker/backends && $(MAKE) down

up: ## Bring up only the backends stack.
	cd ../../../docker/backends && $(MAKE) up

obs-up: ## Bring up only the observability stack.
	cd ../../../docker/observability && $(MAKE) up

psql: ## Open a psql shell on the agent DB.
	docker exec -it mcpkit-postgres psql -U $(PG_USER) -d $(PG_DB)

mem: ## Show the durable semantic-memory rows.
	docker exec mcpkit-postgres psql -U $(PG_USER) -d $(PG_DB) -c "select namespace, key, created_at from agent_memories order by created_at;"

server: ## Boot just the demo MCP server (:8788).
	cd server && go run .

build: ## Build the demo server (compile check).
	cd server && go build ./...
