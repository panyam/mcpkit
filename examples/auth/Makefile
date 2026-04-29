demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the unified MCP server standalone (for MCPJam, VS Code, Claude Desktop)
	go run . --serve

readme: ## Regenerate README.md from demo definitions
	go run . --readme > WALKTHROUGH.md

build: ## Build the binary
	go build -o auth-demo .

# Run individual single-pattern servers (legacy, mostly for testing).
serve-bearer:           ; go run ./bearer
serve-jwt:              ; go run ./jwt
serve-scopes:           ; go run ./scopes
serve-session-binding:  ; go run ./session-binding
serve-public-discovery: ; go run ./public-discovery
serve-unified:          ; go run ./unified

.PHONY: demo serve readme build serve-bearer serve-jwt serve-scopes serve-session-binding serve-public-discovery serve-unified
.DEFAULT_GOAL := demo
