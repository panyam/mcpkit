# whole-enchilada / events — top-level Makefile orchestrating the demo
# stack.
#
# Knobs:
#   make up                       # N=1 event-server replica (committed default)
#   make up N=3                   # 3 event-server replicas
#   make up BUILD=true            # force a fresh docker image build
#
# Synthetic producers (chat / presence) are operator-runnable from the
# host via `make drive-chat` and `make drive-presence` — the stack
# comes up silent and stays that way until you fire a driver. This is
# the "subscribe-first, fire-producers-from-window-A" demo arc.
#
# Hostnames (*.whole-enchilada) resolve inside the compose network out
# of the box. To reach them from the host shell / browser, run
# `make hosts-install` once; `make hosts-uninstall` reverses it.

N ?= 3

# Stage-2: per-tenant subscriber binaries are invoked as
#   make poller   TENANT=A TOKEN=$T
#   make webhook  TENANT=A TOKEN=$T
#   make inject   TENANT=A EVENT=chat.message TEXT="hi"
# TENANT accepts A / B / C (case-insensitive) or a literal realm name
# (e.g. asgard, my-custom-realm). The token comes from `make newtoken`.
TENANT  ?=
TOKEN   ?= $(shell echo $$TOKEN)
EVENT   ?= chat.message
TEXT    ?= hello from `make inject`
USER    ?=
PASSWORD ?=

# Published images the standalone events-stack.yaml pulls. Override REGISTRY
# to push somewhere other than our GHCR org.
# The three realm introspection endpoints the auth profile flips the replicas
# onto. Without this the `auth` profile starts Keycloak and leaves the servers
# anonymous, because OAUTH_INTROSPECTION_URLS defaults to empty in the stack.
AUTH_INTROSPECTION_URLS ?= http://keycloak:8080/realms/asgard/protocol/openid-connect/token/introspect,http://keycloak:8080/realms/babylon/protocol/openid-connect/token/introspect,http://keycloak:8080/realms/camelot/protocol/openid-connect/token/introspect

REGISTRY         ?= ghcr.io/panyam
IMAGE_TAG        ?= latest
EVENT_SERVER_IMAGE ?= $(REGISTRY)/mcpkit-event-server:$(IMAGE_TAG)
KEYCLOAK_IMAGE     ?= $(REGISTRY)/mcpkit-events-keycloak:$(IMAGE_TAG)

# Host port for the stack's Keycloak. docker/backends publishes one on 8180
# too (for examples/agents), so set KEYCLOAK_PORT to run both at once.
KEYCLOAK_PORT    ?= 8180
KEYCLOAK_HOST    ?= http://localhost:$(KEYCLOAK_PORT)
ONEAUTH_VERSION  ?= v0.1.36
ONEAUTH          := oneauth
DEMO_CLIENT_ID   ?= mcp-events-poller
DEMO_CLIENT_SECRET ?= mcpkit-demo-secret-DEMO-ONLY

# Shared infra composes live under ../../../docker/ and run as their own
# projects. `make up` does not touch them any more: the events stack owns its
# postgres, redis and keycloak since the compose merge. Observability stays
# optional and operator-managed, via `make obsup` or
# `cd ../../../docker/observability && make up`.
# events-stack.yaml is the ONE canonical stack; compose.dev.yaml overlays it
# for development (builds from the checkout, publishes DB ports, joins the
# shared mcpkit network). Every recipe goes through one of these two, because
# a bare `docker compose` picks up no file now that docker-compose.yaml is gone.
STACK          := events-stack.yaml
DEV_OVERLAY    := compose.dev.yaml
COMPOSE        := docker compose -f $(STACK) -f $(DEV_OVERLAY)
COMPOSE_PUBLIC := docker compose -f $(STACK)

# The events stack owns its own postgres + redis + keycloak now, so
# docker/backends is no longer part of this example's flow. It stays reachable
# for examples/agents, which still shares it.
BACKENDS_COMPOSE      := ../../../docker/backends/docker-compose.yml
OBSERVABILITY_COMPOSE := ../../../docker/observability/docker-compose.yml

# tenant-realm maps a shorthand TENANT letter to the canonical realm
# name. A → asgard, B → babylon, C → camelot. Pass a literal realm
# name (or any custom realm) and it's preserved unchanged.
tenant-realm = $(strip $(if $(filter A a,$(1)),asgard,$(if $(filter B b,$(1)),babylon,$(if $(filter C c,$(1)),camelot,$(1)))))

HOSTS_MARKER := # whole-enchilada hostnames — managed by make hosts-install
HOSTS_NAMES  := nginx.whole-enchilada event-server.whole-enchilada \
                event-server-1.whole-enchilada event-server-2.whole-enchilada event-server-3.whole-enchilada

.PHONY: help gen-compose build unittest up down obsup obsdown test demo readme predemo \
        check-ports smoke stack-up stack-up-auth stack-down stack-smoke stack-smoke-auth stack-logs images images-push \
        newtoken newtoken-ci poller streamer webhook inject drive-chat drive-presence \
        add-discord rm-source list-sources alllogs \
        hosts-install hosts-uninstall logs ps clean clean-backends clear_all_tokens clear-all

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

gen-compose: ## Regenerate events-stack.yaml + compose.dev.yaml for N event-servers.
	go -C tools/gen-compose run . -n=$(N) -out=../.. -image=$(EVENT_SERVER_IMAGE) -keycloak-image=$(KEYCLOAK_IMAGE)

build: ## Build all binaries locally (without Docker). push-server stays a reference for the production-shape HTTPSource pattern (run via `go -C push-server run .`).
	go -C event-server build -o ../bin/event-server ./
	go -C poller build -o ../bin/poller ./
	go -C webhook build -o ../bin/webhook ./
	go -C streamer build -o ../bin/streamer ./
	go -C inject build -o ../bin/inject ./
	go -C drivers/synth build -o ../../bin/synth ./
	go -C evctl build -o ../bin/evctl ./
	go -C walkthrough build -o ../bin/walkthrough ./

unittest: ## In-process Go tests (no Docker needed; covers tenant-isolation + protocol contracts).
	go -C event-server test ./... -count=1 -timeout 60s

obsup: ## Bring up the observability stack (grafana/loki/tempo/mimir/otel-collector). Optional.
	docker compose -f $(OBSERVABILITY_COMPOSE) up -d --wait

obsdown: ## Tear down the observability stack.
	docker compose -f $(OBSERVABILITY_COMPOSE) down

# Fails fast with the fix rather than letting Docker report a bare
# "port is already allocated" from whichever container got there first.
check-ports:
	@for p in 9090 $(KEYCLOAK_PORT); do \
		if lsof -nP -iTCP:$$p -sTCP:LISTEN >/dev/null 2>&1; then \
			echo "port $$p is already in use."; \
			echo "  usually docker/backends (shared with examples/agents) or a stack you left up."; \
			echo "  fixes: 'make stack-down', or 'cd ../../../docker/backends && make down',"; \
			echo "         or run this stack beside it with 'make up KEYCLOAK_PORT=8181'."; \
			exit 1; \
		fi; \
	done

up: gen-compose check-ports ## Bring up the full dev stack (builds from your checkout, Keycloak + auth on). Pass BUILD=true to force a rebuild.
	KEYCLOAK_PORT=$(KEYCLOAK_PORT) OAUTH_ISSUER_BASE="$(KEYCLOAK_HOST)" \
	OAUTH_INTROSPECTION_URLS="$(AUTH_INTROSPECTION_URLS)" \
		$(COMPOSE) --profile auth up -d --wait --wait-timeout 300 $(if $(BUILD),--build)
	@echo "stack up$(if $(BUILD), (rebuilt)); nginx on http://localhost:9090, Keycloak on $(KEYCLOAK_HOST)"
	@echo "postgres on 127.0.0.1:5433, redis on 127.0.0.1:6380 (offset so docker/backends can coexist)"
	@echo "verify with: make smoke"

down: ## Tear down the dev stack. Volumes are preserved — use `make clean` to drop them too.
	$(COMPOSE) --profile auth down

# --- published stack, without the dev overlay -----------------------------
#
# The targets above layer compose.dev.yaml on top, which builds event-server
# from your checkout. The ones below drive events-stack.yaml alone, exactly as
# an external user who curl'd that one file would. Use them to check what we
# actually publish rather than what your working tree produces.

stack-up: ## Bring up the standalone events-stack.yaml (pulls published images; anonymous auth).
	$(COMPOSE_PUBLIC) up -d --wait --wait-timeout 180
	@echo "standalone stack up; nginx on http://localhost:9090 — try: make stack-smoke"

stack-up-auth: ## Same, plus Keycloak and the three demo realms, with the replicas ON token introspection.
	KEYCLOAK_PORT=$(KEYCLOAK_PORT) OAUTH_ISSUER_BASE="$(KEYCLOAK_HOST)" \
	OAUTH_INTROSPECTION_URLS="$(AUTH_INTROSPECTION_URLS)" \
		$(COMPOSE_PUBLIC) --profile auth up -d --wait --wait-timeout 300
	@echo "standalone stack up WITH auth; Keycloak on $(KEYCLOAK_HOST)"
	@echo "verify the replicas actually reject anonymous callers: make stack-smoke-auth"

stack-smoke-auth: ## Assert the auth stack REJECTS an anonymous subscriber (the check stack-smoke cannot make).
	EXPECT_AUTH=1 ./scripts/smoke-stack.sh

stack-down: ## Tear the standalone stack down, including its named volumes.
	$(COMPOSE_PUBLIC) --profile auth down -v

smoke: ## Verify the DEV stack (what `make up` brings up). Auth is on there, so this asserts anonymous callers are refused.
	EXPECT_AUTH=1 ./scripts/smoke-stack.sh

stack-smoke: ## Verify the PUBLISHED stack in its default anonymous posture (`make stack-up`). Not for `make up` — that stack has auth on.
	./scripts/smoke-stack.sh

stack-logs: ## Tail logs from the stack (works for both the dev and published flows).
	$(COMPOSE) logs -f --tail=100

images: ## Build the published images locally (does not push).
	docker build -t $(EVENT_SERVER_IMAGE) -f event-server/Dockerfile ../../..
	docker build -t $(KEYCLOAK_IMAGE) ../../../docker/backends/keycloak

images-push: images ## Build and push the published images. Requires `docker login ghcr.io`.
	docker push $(EVENT_SERVER_IMAGE)
	docker push $(KEYCLOAK_IMAGE)

test: ## Run the walkthrough in non-interactive mode against the running docker stack.
	go -C walkthrough run . --non-interactive

predemo: gen-compose ## Clean slate (run ONCE before the demo): WIPE backend data, rebuild images, restart everything, open Keycloak + Grafana. Wiping the DB is deliberate — schema changes ship "fresh deploys only" (no migrations), so a stale volume can crash event-server on AutoMigrate.
	@COMPOSE="$(COMPOSE)" OBSERVABILITY_COMPOSE=$(OBSERVABILITY_COMPOSE) \
		AUTH_INTROSPECTION_URLS="$(AUTH_INTROSPECTION_URLS)" bash predemo.sh

demo: ## Run the walkthrough interactively against the running stack.
	go -C walkthrough run . --tui

readme: ## Regenerate WALKTHROUGH.md from walkthrough/walkthrough.go.
	go -C walkthrough run . --doc=md > WALKTHROUGH.md

# -------------------------------------------------------------------
# Stage-2 client-side wrappers. The operator runs these from sibling
# terminals while `make up` keeps the docker stack running.
# Token flow:
#   1. `make newtoken TENANT=A`           # interactive browser auth
#   2. `export TA=$(make newtoken TENANT=A --silent ...)` etc.
#   3. `make poller TENANT=A TOKEN=$TA`   # long-running subscriber
# See README "stage 2 quickstart" for the full 4-terminal flow.
# -------------------------------------------------------------------

newtoken: ## Acquire a bearer token via Keycloak browser auth. TENANT=A|B|C. Prints ONLY the access token so `$(make newtoken TENANT=A)` captures cleanly.
	@if [ -z "$(TENANT)" ]; then echo "usage: make newtoken TENANT=A|B|C" >&2; exit 2; fi
	@$(ONEAUTH) token browser $(KEYCLOAK_HOST)/realms/$(call tenant-realm,$(TENANT)) \
		--client-id $(DEMO_CLIENT_ID) \
		--client-secret $(DEMO_CLIENT_SECRET) \
		--format=access-token-only

newtoken-ci: ## Acquire a bearer token via ROPC (CI / scripting). TENANT=A|B|C USER=alice PASSWORD=alice.
	@if [ -z "$(TENANT)" ] || [ -z "$(USER)" ] || [ -z "$(PASSWORD)" ]; then \
		echo "usage: make newtoken-ci TENANT=A|B|C USER=<user> PASSWORD=<pw>" >&2; exit 2; fi
	@$(ONEAUTH) token password $(KEYCLOAK_HOST)/realms/$(call tenant-realm,$(TENANT)) \
		--client-id $(DEMO_CLIENT_ID) \
		--client-secret $(DEMO_CLIENT_SECRET) \
		--user $(USER) \
		--password $(PASSWORD) \
		--format=access-token-only
	@echo

poller: ## Long-running poll subscriber. TENANT=A|B|C plus either TOKEN=<bearer> or USERNAME=<u> [PASSWORD=<p>; defaults to USERNAME]. Resume knob: START_CURSOR=<N> (default 0 = replay from buffer head).
	@if [ -z "$(TOKEN)" ] && [ -z "$(USERNAME)" ]; then \
		echo "usage: make poller TENANT=A|B|C [TOKEN=<bearer> | USERNAME=<u> [PASSWORD=<p>]] [START_CURSOR=<N>]" >&2; exit 2; fi
	go -C poller run . --tenant $(call tenant-realm,$(TENANT)) \
		$(if $(TOKEN),--token $(TOKEN)) \
		$(if $(USERNAME),--username $(USERNAME) --password $(if $(PASSWORD),$(PASSWORD),$(USERNAME))) \
		$(if $(START_CURSOR),--start-cursor $(START_CURSOR)) \
		--event $(EVENT)

webhook: ## Long-running webhook subscriber. TENANT=A|B|C plus either TOKEN=<bearer> or USERNAME=<u> [PASSWORD=<p>; defaults to USERNAME]. Demo knobs: TTL_MS=<int|null> REPLY_STATUS=<int> EXIT_AFTER=<int>.
	@if [ -z "$(TOKEN)" ] && [ -z "$(USERNAME)" ]; then \
		echo "usage: make webhook TENANT=A|B|C [TOKEN=<bearer> | USERNAME=<u> [PASSWORD=<p>]] [TTL_MS=<int|null>] [REPLY_STATUS=<int>] [EXIT_AFTER=<int>]" >&2; exit 2; fi
	go -C webhook run . --tenant $(call tenant-realm,$(TENANT)) \
		$(if $(TOKEN),--token $(TOKEN)) \
		$(if $(USERNAME),--username $(USERNAME) --password $(if $(PASSWORD),$(PASSWORD),$(USERNAME))) \
		--event $(EVENT) \
		$(if $(TTL_MS),--ttl-ms $(TTL_MS)) \
		$(if $(REPLY_STATUS),--reply-status $(REPLY_STATUS)) \
		$(if $(EXIT_AFTER),--exit-after $(EXIT_AFTER))

streamer: ## Long-running events/stream subscriber over the stateless wire (push delivery). TENANT=A|B|C plus either TOKEN=<bearer> or USERNAME=<u> [PASSWORD=<p>; defaults to USERNAME].
	@if [ -z "$(TOKEN)" ] && [ -z "$(USERNAME)" ]; then \
		echo "usage: make streamer TENANT=A|B|C [TOKEN=<bearer> | USERNAME=<u> [PASSWORD=<p>]]" >&2; exit 2; fi
	go -C streamer run . --tenant $(call tenant-realm,$(TENANT)) \
		$(if $(TOKEN),--token $(TOKEN)) \
		$(if $(USERNAME),--username $(USERNAME) --password $(if $(PASSWORD),$(PASSWORD),$(USERNAME))) \
		--event $(EVENT)

# Drivers — operator-runnable synthetic producers. Stack starts silent;
# fire these in sibling terminals to start producing events. drive-chat
# and drive-presence are thin wrappers around one synth binary that
# picks a per-event vocabulary via --event. EVERY tunes the cadence;
# TENANTS rotates round-robin across the comma-separated list (default:
# all three).
EVERY    ?=
TENANTS  ?= asgard,babylon,camelot

drive-chat: ## Synthetic chat.message producer. EVERY=2s (default), TENANTS=asgard,babylon,camelot.
	go -C drivers/synth run . --event chat.message --tenants $(TENANTS) $(if $(EVERY),--every $(EVERY))

drive-presence: ## Synthetic presence.changed producer. EVERY=5s (default), TENANTS=asgard,babylon,camelot.
	go -C drivers/synth run . --event presence.changed --tenants $(TENANTS) $(if $(EVERY),--every $(EVERY))

# -------------------------------------------------------------------
# Dynamic-source admin — operator-runnable, talks to per-replica admin
# endpoints under /admin/replicas/{idx}/ on the nginx frontdoor. Lets
# the operator add / remove real Discord sources on specific event-
# server replicas at runtime, dropping the "subscribe-first, fire-
# producers-from-window-A" arc with REAL upstreams.
#
# Variables (set inline or via env):
#   TOKEN     Discord bot token (required for add-discord)
#   CHANNELS  comma-separated Discord channel IDs (required for add-discord)
#   REPLICAS  comma-separated replica indices (e.g. 1,4). Required for add/rm.
#   NAME      EventDef.Name override (default discord.message)
#   SOURCE    Source name to remove (rm-source).
# -------------------------------------------------------------------

REPLICAS ?= 1
NAME     ?= discord.message

add-discord: ## Register a real Discord source on specified replicas. TOKEN=... CHANNELS=A,B REPLICAS=1,4 [TENANTS=a,c] [NAME=discord.message].
	@if [ -z "$(TOKEN)" ] || [ -z "$(CHANNELS)" ] || [ -z "$(REPLICAS)" ]; then \
		echo "usage: make add-discord TOKEN=<bot-token> CHANNELS=<id,id> REPLICAS=<idx,idx> [TENANTS=<a,c>] [NAME=<source>]" >&2; exit 2; fi
	go -C evctl run . sources add discord \
		--token=$(TOKEN) --channels=$(CHANNELS) --tenants=$(TENANTS) \
		--replicas=$(REPLICAS) --name=$(NAME)

rm-source: ## Remove a runtime-added source. SOURCE=<name> REPLICAS=<idx,idx>.
	@if [ -z "$(SOURCE)" ] || [ -z "$(REPLICAS)" ]; then \
		echo "usage: make rm-source SOURCE=<name> REPLICAS=<idx,idx>" >&2; exit 2; fi
	go -C evctl run . sources rm $(SOURCE) --replicas=$(REPLICAS)

list-sources: ## List sources known to a replica. REPLICAS=<idx>.
	go -C evctl run . sources list --replicas=$(REPLICAS)

iterm: ## macOS+iTerm2 only — open a 2x3 grid: row1 = pollers A/B/C, row2 = webhooks A/B/C.
	@bash ./iterm-6up.sh

alllogs: ## macOS+iTerm2 only — open one iTerm window with 3 vertically-stacked panes tailing backends / observability / events logs.
	@bash ./iterm-logs.sh

clear_all_tokens: ## Sign out every USER session in all 3 tenant realms (keeps mcp-event-server client creds intact).
	@$(COMPOSE) exec -T keycloak /opt/keycloak/bin/kcadm.sh config credentials \
		--server http://keycloak:8080 --realm master --user admin --password admin >/dev/null
	@for realm in asgard babylon camelot; do \
		$(COMPOSE) exec -T keycloak /opt/keycloak/bin/kcadm.sh create logout-all -r $$realm >/dev/null && \
		echo "[clear_all_tokens] $$realm: all user sessions signed out"; \
	done

clear-all: clear_all_tokens ## Quick clean slate without a full predemo: sign out Keycloak sessions + delete Redis quota keys + truncate webhooks. Survivors of Ctrl+C from prior demo runs (counters/subs that didn't release cleanly) get wiped so the cap=3 quota resets and stale callback URLs disappear. Heavier hammer if needed: stop the stack and `make clean`.
	@deleted=$$($(COMPOSE) exec -T redis sh -c 'redis-cli --scan --pattern "mcpkit.events.quota.*" | xargs -r redis-cli DEL' 2>/dev/null || echo 0); \
		echo "[clear-all] redis quota keys deleted: $$deleted"
	@$(COMPOSE) exec -T postgres psql -U postgres -d events -c "TRUNCATE TABLE webhooks" >/dev/null 2>&1 && \
		echo "[clear-all] webhooks table truncated" || \
		echo "[clear-all] webhooks table not present yet (no subs ever created) — skip"

inject: ## Inject one event from the host. TENANT=A|B|C [EVENT=chat.message] [TEXT="..."].
	@if [ -z "$(TENANT)" ]; then echo "usage: make inject TENANT=A|B|C [EVENT=<name>] [TEXT='...']"; exit 2; fi
	go -C inject run . --tenant $(call tenant-realm,$(TENANT)) --event $(EVENT) --text $(call shellquote,$(TEXT))

# shellquote wraps a string so the shell sees one argument, even
# if it contains spaces. Used by `make inject TEXT="hello world"`.
shellquote = '$(subst ','\'',$(1))'

logs: ## Tail logs from all services.
	$(COMPOSE) logs -f --tail=200

ps: ## docker compose ps.
	$(COMPOSE) ps

# Demo helpers for the TTL / no-expiry / failure-GC walkthrough beats
# (PRs 778, 779, 783). Each is a one-liner pointed at the running
# compose stack — no surprises.

psql-webhooks: ## Print the webhooks table from Postgres — used by the no-expiry restart-survival beat to confirm rows survive a `make restart-event-servers`.
	$(COMPOSE) exec -T postgres \
		psql -U postgres -d events \
		-c "SELECT id, principal, url, expires_at, status_failing_continuously_since FROM webhooks ORDER BY principal;"

restart-event-servers: ## Rolling-restart the event-server replicas. Used by the no-expiry restart-survival beat — finite subs and no-expiry subs both survive (rows stay in Postgres), proving the GORM-backed WebhookStore is the durability backbone. Replica set is discovered from compose, so it tracks whatever N the stack was generated with.
	@svcs=$$($(COMPOSE) config --services | grep '^event-server-'); \
		echo "[restart-event-servers] restarting: $$svcs"; \
		$(COMPOSE) restart $$svcs && \
		$(COMPOSE) up -d --wait $$svcs

clean: ## Tear down the dev stack AND drop its postgres/redis volumes + built binaries. This IS the full reset now.
	$(COMPOSE) --profile auth down -v 2>/dev/null || true
	rm -rf bin/

clean-backends: ## Wipe the SHARED docker/backends data. NOT needed for events any more — this only affects examples/agents.
	@echo "[clean-backends] events owns its own postgres/redis volumes since the compose merge."
	@echo "[clean-backends] this wipes docker/backends, which examples/agents shares. Use 'make clean' for events."
	@printf 'proceed? [y/N] ' && read ans && [ "$$ans" = "y" ] || exit 1
	docker compose -f $(BACKENDS_COMPOSE) down -v 2>/dev/null || true
	rm -rf ../../../docker/backends/data/

# Host /etc/hosts management — uses a marker comment so the edit is
# idempotent and reversible. Requires sudo.
hosts-install: ## Append *.whole-enchilada hostnames to /etc/hosts (idempotent).
	@if grep -q "whole-enchilada hostnames" /etc/hosts; then \
		echo "[hosts] already installed; run hosts-uninstall first to refresh"; \
	else \
		sudo sh -c 'printf "\n# whole-enchilada hostnames — managed by make hosts-install\n127.0.0.1 $(HOSTS_NAMES)\n" >> /etc/hosts'; \
		echo "[hosts] installed: $(HOSTS_NAMES)"; \
	fi

hosts-uninstall: ## Remove the whole-enchilada /etc/hosts entries.
	@sudo sed -i.bak '/whole-enchilada hostnames/,+1 d' /etc/hosts && rm -f /etc/hosts.bak
	@echo "[hosts] uninstalled"
