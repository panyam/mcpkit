// Command agentchat is a terminal chat harness over any set of MCP servers:
// point it at a config (or a server URL) and an OpenAI-compatible model, and
// converse with live tool calls, elicitation prompts, and streaming output.
// It is the reference in-process surface for the agent module.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/panyam/mcpkit/agent/host"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const version = "0.1.0"

// newRoot builds the CLI. Every flag is env-overridable with the AGENTCHAT_
// prefix (dashes become underscores: --base-url is AGENTCHAT_BASE_URL);
// precedence is flag over env over default, per viper.
func newRoot() (*cobra.Command, *viper.Viper) {
	v := viper.New()
	v.SetEnvPrefix("AGENTCHAT")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	root := &cobra.Command{
		Use:          "agentchat",
		Short:        "terminal chat over any set of MCP servers",
		Long:         "agentchat connects MCP servers and an OpenAI-compatible model into a terminal agent:\nlive tool calls, streamed output, schema-driven elicitation prompts, per-server allowlists.",
		Version:      version,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(v)
		},
	}

	fl := root.Flags()
	fl.String("config", "", "path to agentchat JSON config")
	fl.StringSlice("url", nil, "MCP server URL (repeatable; used when no config)")
	fl.String("base-url", "http://localhost:1234/v1", "OpenAI-compatible endpoint (when no config)")
	fl.String("model", "", "model identifier (when no config)")
	fl.String("api-key-env", "", "env var holding the model API key")
	fl.String("instructions", "You are a helpful assistant with access to tools.", "system prompt (when no config)")
	fl.Int("max-steps", 0, "max model calls per turn (0 = default)")
	fl.String("exporter", "", "telemetry exporter: stdout | otlp | auto (empty = off)")
	fl.String("otlp-endpoint", "", "OTLP gRPC endpoint (default localhost:4317)")
	if err := v.BindPFlags(fl); err != nil {
		panic(err)
	}
	return root, v
}

func runChat(v *viper.Viper) error {
	cfg, err := buildConfig(
		v.GetString("config"),
		v.GetStringSlice("url"),
		v.GetString("base-url"),
		v.GetString("model"),
		v.GetString("api-key-env"),
		v.GetString("instructions"),
		v.GetInt("max-steps"),
	)
	if err != nil {
		return err
	}

	ctx := context.Background()
	tp, tpShutdown, err := SetupTelemetry(ctx,
		WithServiceName("agentchat"),
		WithExporter(v.GetString("exporter")),
		WithOTLPEndpoint(v.GetString("otlp-endpoint")),
	)
	if err != nil {
		return err
	}
	defer tpShutdown(context.Background())
	logger, logShutdown, err := SetupLogs(ctx,
		WithServiceName("agentchat"),
		WithExporter(v.GetString("exporter")),
		WithOTLPEndpoint(v.GetString("otlp-endpoint")),
	)
	if err != nil {
		return err
	}
	defer logShutdown(context.Background())

	app, err := host.NewApp(cfg, os.Stdout, os.Stdin,
		host.WithTracerProvider(tp),
		host.WithLogger(logger),
	)
	if err != nil {
		return err
	}
	defer app.Close()

	fmt.Printf("agentchat: %d server(s), model %s. /tools /history /quit; Ctrl-C cancels a turn.\n", len(cfg.Servers), cfg.Model.Model)

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

	return app.REPL(context.Background(), os.Stdin, turnCtx)
}

func main() {
	root, _ := newRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agentchat:", err)
		os.Exit(1)
	}
}

// buildConfig merges the config file with quick-start settings: a config
// file wins for everything it specifies; url/model cover the no-file case.
func buildConfig(path string, urls []string, baseURL, model, apiKeyEnv, instructions string, maxSteps int) (*host.Config, error) {
	if path != "" {
		return host.LoadConfig(path)
	}
	if len(urls) == 0 || model == "" {
		return nil, fmt.Errorf("agentchat: need --config, or --model plus at least one --url")
	}
	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: baseURL, Model: model, APIKeyEnv: apiKeyEnv},
		Instructions: instructions,
		MaxSteps:     maxSteps,
	}
	for i, u := range urls {
		cfg.Servers = append(cfg.Servers, host.ServerConfig{ID: fmt.Sprintf("srv%d", i+1), URL: u})
	}
	return cfg, cfg.Validate()
}
