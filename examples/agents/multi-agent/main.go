package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/panyam/mcpkit/agent"
)

func main() {
	serveFlag := flag.Bool("serve", false, "run the demo MCP server (for the live chat/web surfaces) instead of the scripted scenario")
	addr := flag.String("addr", ":8785", "listen address for --serve")
	model := flag.String("model", "", "OpenAI-compatible model for a live supervisor (default: deterministic stub)")
	baseURL := flag.String("base-url", "http://localhost:1234/v1", "model endpoint for --model")
	apiKeyEnv := flag.String("api-key-env", "", "env var holding the model API key (never the key itself)")
	flag.Parse()

	// --serve boots the demo MCP server the config.json personas delegate over
	// (just chat / just web). The scripted scenario below is the golden test.
	if *serveFlag {
		if err := serve(*addr); err != nil {
			fmt.Fprintln(os.Stderr, "multi-agent:", err)
			os.Exit(1)
		}
		return
	}

	var provider agent.Provider
	if *model != "" {
		p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{BaseURL: *baseURL, Model: *model, APIKey: os.Getenv(*apiKeyEnv)})
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
