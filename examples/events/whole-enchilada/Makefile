# whole-enchilada — top-level Makefile orchestrating the demo stack.
#
# Replica counts are knobs:
#   make demo-up                  # N=1 M=1 (committed default)
#   make demo-up N=3 M=2          # 3 event-servers, 2 push-servers
#
# Hostnames (*.whole_enchilada) resolve inside the compose network out of
# the box. To reach them from the host shell / browser, run
# `make hosts-install` once; `make hosts-uninstall` reverses it.

N ?= 1
M ?= 1

HOSTS_MARKER := # whole-enchilada hostnames — managed by make hosts-install
HOSTS_NAMES  := nginx.whole_enchilada event_server.whole_enchilada \
                event_server_1.whole_enchilada event_server_2.whole_enchilada event_server_3.whole_enchilada \
                pusher.whole_enchilada pusher_1.whole_enchilada pusher_2.whole_enchilada \
                receiver.whole_enchilada

.PHONY: help gen-compose build test demo-up demo-down demo-test demo readme \
        hosts-install hosts-uninstall logs ps clean

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

gen-compose: ## Regenerate docker-compose.yaml + nginx/nginx.conf for N event-servers, M push-servers.
	go -C tools/gen-compose run . -n=$(N) -m=$(M) -out=../..

build: ## Build all four binaries locally (without Docker).
	go -C event-server build -o ../bin/event-server ./
	go -C push-server build -o ../bin/push-server ./
	go -C receiver build -o ../bin/receiver ./
	go -C walkthrough build -o ../bin/walkthrough ./

test: ## In-process e2e tests (no Docker needed).
	go -C event-server test ./... -count=1 -timeout 60s

demo-up: gen-compose ## docker compose up -d, wait for healthchecks. Honors N + M.
	docker compose up -d --build
	@echo "stack up; nginx on http://localhost:8080 — see README for hostname routing"

demo-down: ## docker compose down -v (removes named volumes too).
	docker compose down -v

demo-test: ## Run the walkthrough in non-interactive mode against the running stack.
	go -C walkthrough run . --non-interactive

demo: ## Run the walkthrough interactively against the running stack.
	go -C walkthrough run . --tui

readme: ## Regenerate WALKTHROUGH.md from walkthrough/walkthrough.go.
	go -C walkthrough run . --doc=md > WALKTHROUGH.md

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
