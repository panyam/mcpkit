// Command agentchat is a terminal chat harness over any set of MCP servers:
// point it at a config (or a server URL) and an OpenAI-compatible model, and
// converse with live tool calls, elicitation prompts, and streaming output.
// It is the reference in-process surface for the agent module.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	var (
		configPath   = flag.String("config", "", "path to agentchat JSON config")
		baseURL      = flag.String("base-url", "http://localhost:1234/v1", "OpenAI-compatible endpoint (when no config)")
		model        = flag.String("model", "", "model identifier (when no config)")
		apiKeyEnv    = flag.String("api-key-env", "", "env var holding the model API key")
		instructions = flag.String("instructions", "You are a helpful assistant with access to tools.", "system prompt (when no config)")
		maxSteps     = flag.Int("max-steps", 0, "max model calls per turn (0 = default)")
	)
	var urls urlList
	flag.Var(&urls, "url", "MCP server URL (repeatable; used when no config)")
	flag.Parse()

	cfg, err := buildConfig(*configPath, urls, *baseURL, *model, *apiKeyEnv, *instructions, *maxSteps)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	app, err := NewApp(cfg, os.Stdout, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer app.Close()

	fmt.Printf("agentchat: %d server(s), model %s. /tools /history /quit; Ctrl-C cancels a turn.\n", len(cfg.Servers), cfg.Model.Model)

	// SIGINT cancels the in-flight turn; a second within an idle prompt
	// exits via default handling once we stop catching.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	turnCtx := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-sigs:
				cancel()
			case <-ctx.Done():
			}
		}()
		return ctx, cancel
	}

	if err := app.REPL(context.Background(), os.Stdin, turnCtx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type urlList []string

func (u *urlList) String() string     { return strings.Join(*u, ",") }
func (u *urlList) Set(s string) error { *u = append(*u, s); return nil }

// buildConfig merges the config file with quick-start flags: a config file
// wins for everything it specifies; --url/--model cover the no-file case.
func buildConfig(path string, urls []string, baseURL, model, apiKeyEnv, instructions string, maxSteps int) (*Config, error) {
	if path != "" {
		return LoadConfig(path)
	}
	if len(urls) == 0 || model == "" {
		return nil, fmt.Errorf("agentchat: need --config, or --model plus at least one --url")
	}
	cfg := &Config{
		Model:        ModelConfig{BaseURL: baseURL, Model: model, APIKeyEnv: apiKeyEnv},
		Instructions: instructions,
		MaxSteps:     maxSteps,
	}
	for i, u := range urls {
		cfg.Servers = append(cfg.Servers, ServerConfig{ID: fmt.Sprintf("srv%d", i+1), URL: u})
	}
	return cfg, cfg.Validate()
}
