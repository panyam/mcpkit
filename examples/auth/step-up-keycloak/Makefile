PORT ?= 3020
KEYCLOAK_URL ?= http://localhost:8180
KC_REALM ?= mcpkit-test

.PHONY: help run build smoke clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run: ## Start the SUT (expects Keycloak already running at $(KEYCLOAK_URL))
	go run . -addr :$(PORT) -keycloak-url $(KEYCLOAK_URL) -realm $(KC_REALM)

build: ## Compile the SUT to ./step-up-keycloak-sut
	go build -o step-up-keycloak-sut .

smoke: ## curl-test the 403 path with an under-scoped token
	@TOKEN=$$(curl -fsS -X POST $(KEYCLOAK_URL)/realms/$(KC_REALM)/protocol/openid-connect/token \
		-d "grant_type=client_credentials" \
		-d "client_id=mcp-confidential" \
		-d "client_secret=mcp-test-secret-for-confidential" \
		-d "scope=openid tools-read" | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])"); \
	echo "--- under-scoped (expect 403 + WWW-Authenticate) ---"; \
	curl -i -X POST http://localhost:$(PORT)/mcp \
		-H "Content-Type: application/json" \
		-H "Authorization: Bearer $$TOKEN" \
		-d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin_call","arguments":{}}}' 2>&1 | head -15

clean: ## Remove built binary
	rm -f step-up-keycloak-sut
