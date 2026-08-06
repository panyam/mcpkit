package main

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/panyam/mcpkit/agent"
)

func main() {
	serveFlag := flag.Bool("serve", false, "run the demo MCP server (for the live chat/web surfaces) instead of the scripted scenario")
	addr := flag.String("addr", ":8786", "listen address for --serve")
	model := flag.String("model", "", "OpenAI-compatible model for the PRIMARY agent (default: deterministic stub)")
	criticModel := flag.String("critic-model", "", "OpenAI-compatible model for the CRITIC (default: deterministic stub)")
	baseURL := flag.String("base-url", "http://localhost:1234/v1", "model endpoint for --model / --critic-model")
	apiKeyEnv := flag.String("api-key-env", "", "env var holding the model API key (never the key itself)")
	flag.Parse()

	// --serve boots the ops MCP server the config.json surfaces point at
	// (just chat / just web). The critic loop itself is code-level composition
	// (see scenario.go / README): config.json drives the primary agent + server.
	if *serveFlag {
		if err := serve(*addr); err != nil {
			fmt.Fprintln(os.Stderr, "critic:", err)
			os.Exit(1)
		}
		return
	}

	newProvider := func(m string) agent.Provider {
		if m == "" {
			return nil
		}
		p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{BaseURL: *baseURL, Model: m, APIKey: os.Getenv(*apiKeyEnv)})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return p
	}

	out := &syncWriter{}
	res, err := runScenario(out, newProvider(*model), newProvider(*criticModel))
	if err != nil {
		fmt.Fprintln(os.Stderr, "critic:", err)
		os.Exit(1)
	}
	fmt.Print(out.String())
	fmt.Printf("\n[critic delivered %d note(s)]\n", len(res.delivered))
}

// syncWriter is a concurrency-safe transcript buffer.
type syncWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}
