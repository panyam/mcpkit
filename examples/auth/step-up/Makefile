PORT ?= 3021
# Defaults to a local Keycloak realm (free, hermetic). For Okta:
#   source .../okta.env && make run ISSUER="$$OKTA_ISSUER" AUDIENCE=api://default
ISSUER ?= http://localhost:8180/realms/mcpkit-test
# AUDIENCE: empty for Keycloak; api://default for Okta's default custom AS.
AUDIENCE ?=

.PHONY: help run build clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run: ## Start the SUT against $(ISSUER) (expects the AS reachable)
	go run . -addr :$(PORT) -issuer "$(ISSUER)" -audience "$(AUDIENCE)"

build: ## Compile the SUT to ./step-up-sut
	go build -o step-up-sut .

clean: ## Remove built binary
	rm -f step-up-sut
