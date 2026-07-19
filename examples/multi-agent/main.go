package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/panyam/mcpkit/agent"
)

func main() {
	model := flag.String("model", "", "OpenAI-compatible model for a live supervisor (default: deterministic stub)")
	baseURL := flag.String("base-url", "http://localhost:1234/v1", "model endpoint for --model")
	flag.Parse()

	var provider agent.Provider
	if *model != "" {
		p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{BaseURL: *baseURL, Model: *model})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		provider = p
	}
	if err := runScenario(os.Stdout, provider); err != nil {
		fmt.Fprintln(os.Stderr, "multi-agent:", err)
		os.Exit(1)
	}
}
