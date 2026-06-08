# whole-enchilada — top-level Makefile orchestrating the demo stack.
#
# Replica counts are knobs:
#   make up                       # N=1 M=1 (committed default)
#   make up N=3 M=2               # 3 event-servers, 2 push-servers
#   make up BUILD=true            # force a fresh docker image build
#
# Hostnames (*.whole_enchilada) resolve inside the compose network out of
# the box. To reach them from the host shell / browser, run
# `make hosts-install` once; `make hosts-uninstall` reverses it.

N ?= 1
M ?= 1

# Stage-2: per-tenant subscriber binaries are invoked as
#   make poller   TENANT=A TOKEN=$T
#   make webhook  TENANT=A TOKEN=$T
#   make inject   TENANT=A EVENT=chat.message TEXT="hi"
# TENANT accepts A / B / C (case-insensitive) or a literal realm name
# (e.g. tenant-a, my-custom-realm). The token comes from `make newtoken`.
TENANT  ?=
TOKEN   ?= $(shell echo $$TOKEN)
EVENT   ?= chat.message
TEXT    ?= hello from `make inject`
USER    ?=
PASSWORD ?=

KEYCLOAK_HOST    ?= http://localhost:8180
ONEAUTH_VERSION  ?= v0.1.17
ONEAUTH          := go run github.com/panyam/oneauth/cmd/oneauth@$(ONEAUTH_VERSION)
DEMO_CLIENT_ID   ?= mcp-events-poller
DEMO_CLIENT_SECRET ?= mcpkit-demo-secret-DEMO-ONLY

# tenant-realm derives a lowercase realm name from TENANT. Pass
# TENANT=A → tenant-a; TENANT=tenant-x → tenant-x (preserves
# already-prefixed names).
tenant-realm = $(if $(filter tenant-%,$(1)),$(1),tenant-$(shell echo "$(1)" | tr A-Z a-z))

HOSTS_MARKER := # whole-enchilada hostnames — managed by make hosts-install
HOSTS_NAMES  := nginx.whole_enchilada event_server.whole_enchilada \
                event_server_1.whole_enchilada event_server_2.whole_enchilada event_server_3.whole_enchilada \
                pusher.whole_enchilada pusher_1.whole_enchilada pusher_2.whole_enchilada \
                receiver.whole_enchilada keycloak.whole_enchilada

.PHONY: help gen-compose build unittest up down test demo readme \
        newtoken newtoken-ci poller webhook inject \
        hosts-install hosts-uninstall logs ps clean

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

gen-compose: ## Regenerate docker-compose.yaml + nginx/nginx.conf for N event-servers, M push-servers.
	go -C tools/gen-compose run . -n=$(N) -m=$(M) -out=../..

build: ## Build all binaries locally (without Docker).
	go -C event-server build -o ../bin/event-server ./
	go -C push-server build -o ../bin/push-server ./
	go -C receiver build -o ../bin/receiver ./
	go -C poller build -o ../bin/poller ./
	go -C webhook build -o ../bin/webhook ./
	go -C inject build -o ../bin/inject ./
	go -C walkthrough build -o ../bin/walkthrough ./

unittest: ## In-process Go tests (no Docker needed; covers tenant-isolation + protocol contracts).
	go -C event-server test ./... -count=1 -timeout 60s

up: gen-compose ## docker compose up -d. Reuses cached images. Pass BUILD=true to force a rebuild after code changes.
	docker compose up -d $(if $(BUILD),--build)
	@echo "stack up$(if $(BUILD), (rebuilt)); nginx on http://localhost:9090 — see README for hostname routing"

down: ## docker compose down -v (removes named volumes too).
	docker compose down -v

test: ## Run the walkthrough in non-interactive mode against the running docker stack.
	go -C walkthrough run . --non-interactive

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

newtoken: ## Acquire a bearer token via Keycloak browser auth. TENANT=A|B|C.
	@if [ -z "$(TENANT)" ]; then echo "usage: make newtoken TENANT=A|B|C"; exit 2; fi
	$(ONEAUTH) token browser $(KEYCLOAK_HOST)/realms/$(call tenant-realm,$(TENANT)) \
		--client-id $(DEMO_CLIENT_ID) \
		--client-secret $(DEMO_CLIENT_SECRET) \
		--format=access-token-only

newtoken-ci: ## Acquire a bearer token via ROPC (CI / scripting). TENANT=A|B|C USER=alice PASSWORD=alice.
	@if [ -z "$(TENANT)" ] || [ -z "$(USER)" ] || [ -z "$(PASSWORD)" ]; then \
		echo "usage: make newtoken-ci TENANT=A|B|C USER=<user> PASSWORD=<pw>"; exit 2; fi
	$(ONEAUTH) token password $(KEYCLOAK_HOST)/realms/$(call tenant-realm,$(TENANT)) \
		--client-id $(DEMO_CLIENT_ID) \
		--client-secret $(DEMO_CLIENT_SECRET) \
		--user $(USER) \
		--password $(PASSWORD) \
		--format=access-token-only

poller: ## Long-running poll subscriber. TENANT=A|B|C TOKEN=<bearer>.
	@if [ -z "$(TOKEN)" ]; then echo "usage: make poller TENANT=A|B|C TOKEN=<bearer>"; exit 2; fi
	go -C poller run . --tenant $(call tenant-realm,$(TENANT)) --token $(TOKEN) --event $(EVENT)

webhook: ## Long-running webhook subscriber. TENANT=A|B|C TOKEN=<bearer>.
	@if [ -z "$(TOKEN)" ]; then echo "usage: make webhook TENANT=A|B|C TOKEN=<bearer>"; exit 2; fi
	go -C webhook run . --tenant $(call tenant-realm,$(TENANT)) --token $(TOKEN) --event $(EVENT)

inject: ## Inject one event from the host. TENANT=A|B|C [EVENT=chat.message] [TEXT="..."].
	@if [ -z "$(TENANT)" ]; then echo "usage: make inject TENANT=A|B|C [EVENT=<name>] [TEXT='...']"; exit 2; fi
	go -C inject run . --tenant $(call tenant-realm,$(TENANT)) --event $(EVENT) --text $(call shellquote,$(TEXT))

# shellquote wraps a string so the shell sees one argument, even
# if it contains spaces. Used by `make inject TEXT="hello world"`.
shellquote = '$(subst ','\'',$(1))'

logs: ## Tail logs from all services.
	docker compose logs -f --tail=200

ps: ## docker compose ps.
	docker compose ps

clean: ## Remove built binaries.
	rm -rf bin/

# Host /etc/hosts management — uses a marker comment so the edit is
# idempotent and reversible. Requires sudo.
hosts-install: ## Append *.whole_enchilada hostnames to /etc/hosts (idempotent).
	@if grep -q "whole-enchilada hostnames" /etc/hosts; then \
		echo "[hosts] already installed; run hosts-uninstall first to refresh"; \
	else \
		sudo sh -c 'printf "\n# whole-enchilada hostnames — managed by make hosts-install\n127.0.0.1 $(HOSTS_NAMES)\n" >> /etc/hosts'; \
		echo "[hosts] installed: $(HOSTS_NAMES)"; \
	fi

hosts-uninstall: ## Remove the whole-enchilada /etc/hosts entries.
	@sudo sed -i.bak '/whole-enchilada hostnames/,+1 d' /etc/hosts && rm -f /etc/hosts.bak
	@echo "[hosts] uninstalled"
