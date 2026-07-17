agent: ## Run the scripted agent scenario (no LLM): an agent drives the task tools through chat
	go run . --agent

agent-live: ## Run the agent scenario against a live model (MODEL=<name> [BASE_URL=<url>])
	go run . --agent --model $(MODEL) $(if $(BASE_URL),--base-url $(BASE_URL))

agent-test: ## Run the agent golden-transcript test
	go test ./... -count=1 -run TestAgentScenario

demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the v2 tasks server standalone (for MCPJam, VS Code, Claude Desktop)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o tasks-v2-demo .

.PHONY: agent agent-live agent-test demo note serve readme build
.DEFAULT_GOAL := demo
