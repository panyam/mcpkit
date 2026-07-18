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

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	gormstore "github.com/panyam/mcpkit/agent/store/gorm"
	redisstore "github.com/panyam/mcpkit/agent/store/redis"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// buildRunStore maps --session-store to a backend: "" is persistence
// off, "memory" gives in-process resume/fork (dies with the process),
// and the rest give restart-surviving sessions — sqlite needs no server
// at all (a local file), redis and postgres point at running ones.
func buildRunStore(spec string) (agent.RunStore, error) {
	switch {
	case spec == "":
		return nil, nil
	case spec == "memory":
		return agent.NewInMemoryRunStore(), nil
	case strings.HasPrefix(spec, "redis://"):
		addr := strings.TrimPrefix(spec, "redis://")
		if addr == "" {
			return nil, fmt.Errorf("agentchat: --session-store redis:// needs host:port")
		}
		return redisstore.New(redis.NewClient(&redis.Options{Addr: addr})), nil
	case strings.HasPrefix(spec, "sqlite://"):
		path := strings.TrimPrefix(spec, "sqlite://")
		if path == "" {
			return nil, fmt.Errorf("agentchat: --session-store sqlite:// needs a file path")
		}
		return openGormStore(sqlite.Open(path + "?_busy_timeout=5000"))
	case strings.HasPrefix(spec, "postgres://") || strings.HasPrefix(spec, "postgresql://"):
		// gorm's postgres driver accepts the URL DSN verbatim.
		return openGormStore(postgres.Open(spec))
	default:
		return nil, fmt.Errorf("agentchat: unknown --session-store %q (want memory, sqlite://path.db, redis://host:port, or postgres://user:pass@host:port/db)", spec)
	}
}

// openGormStore opens the dialector with SQL logging silenced (the
// transcript is the CLI's output; slow-query noise does not belong in
// it) and wraps it in the RunStore.
func openGormStore(dial gorm.Dialector) (agent.RunStore, error) {
	db, err := openGormDB(dial)
	if err != nil {
		return nil, err
	}
	return gormstore.New(db)
}

// openGormDB opens a GORM handle with SQL logging silenced, shared by the
// run store and the tool-result store so a single --session-store spec
// backs both on one connection.
func openGormDB(dial gorm.Dialector) (*gorm.DB, error) {
	db, err := gorm.Open(dial, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("agentchat: opening store: %w", err)
	}
	return db, nil
}

// buildToolResultStore chooses the offload blob backend. A non-empty dir
// wins: offloaded results become files under it, the no-server local path
// (see agent.FileToolResultStore). Otherwise it maps --session-store so
// blobs share the run store's backend: "" / "memory" give the in-memory
// store (blobs die with the process), sqlite / redis / postgres give
// restart-surviving blobs. A nil return means "use the host's in-memory
// default".
func buildToolResultStore(spec, dir string) (agent.ToolResultStore, error) {
	if dir != "" {
		return agent.NewFileToolResultStore(dir)
	}
	switch {
	case spec == "" || spec == "memory":
		return nil, nil
	case strings.HasPrefix(spec, "redis://"):
		addr := strings.TrimPrefix(spec, "redis://")
		if addr == "" {
			return nil, fmt.Errorf("agentchat: --session-store redis:// needs host:port")
		}
		return redisstore.NewToolResultStore(redis.NewClient(&redis.Options{Addr: addr})), nil
	case strings.HasPrefix(spec, "sqlite://"):
		path := strings.TrimPrefix(spec, "sqlite://")
		if path == "" {
			return nil, fmt.Errorf("agentchat: --session-store sqlite:// needs a file path")
		}
		db, err := openGormDB(sqlite.Open(path + "?_busy_timeout=5000"))
		if err != nil {
			return nil, err
		}
		return gormstore.NewToolResultStore(db)
	case strings.HasPrefix(spec, "postgres://") || strings.HasPrefix(spec, "postgresql://"):
		db, err := openGormDB(postgres.Open(spec))
		if err != nil {
			return nil, err
		}
		return gormstore.NewToolResultStore(db)
	default:
		return nil, fmt.Errorf("agentchat: unknown --session-store %q", spec)
	}
}

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
	fl.String("session-store", "", "session persistence backend: memory | sqlite://path.db | redis://host:port | postgres://user:pass@host:port/db (empty = off)")
	fl.String("session", "", "session run ID to create or resume at startup (needs --session-store)")
	fl.String("ui", "auto", "interface: auto (TUI when interactive) | tui | plain")
	fl.Int("offload-threshold", 0, "offload tool results at/over N bytes to a store, feeding the model a stub + read_tool_result (0 = off; blobs use --session-store's backend)")
	fl.String("offload-dir", "", "store offloaded tool results as files under this directory (no server needed); overrides --session-store for blobs")
	fl.Bool("memory", false, "enable working memory: remember/recall/forget tools the model manages across turns (in-memory store)")
	fl.Bool("memory-inject-summary", false, "with --memory, prepend a summary of the scratchpad before each turn (costs tokens; off = recall on demand)")
	fl.Int("compact-tokens", 0, "compact history when its estimated token count exceeds N: summarize the head, keep a recent tail (0 = off)")
	fl.Int("compact-keep-recent", 0, "with --compact-tokens, how many trailing messages to keep verbatim (0 = default 6)")
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

	appOpts := []host.AppOption{
		host.WithTracerProvider(tp),
		host.WithLogger(logger),
	}
	tuiMode := wantTUI(v.GetString("ui"))
	var surface *tuiObserver
	if tuiMode {
		surface = newTUIObserver()
		appOpts = append(appOpts, host.WithObserver(surface))
	}
	sessionStore := v.GetString("session-store")
	store, err := buildRunStore(sessionStore)
	if err != nil {
		return err
	}
	if store != nil {
		appOpts = append(appOpts, host.WithRunStore(store))
	}

	if threshold := v.GetInt("offload-threshold"); threshold > 0 {
		cfg.Offload = &host.OffloadConfig{ThresholdBytes: threshold}
		trStore, err := buildToolResultStore(sessionStore, v.GetString("offload-dir"))
		if err != nil {
			return err
		}
		if trStore != nil {
			appOpts = append(appOpts, host.WithToolResultStore(trStore))
		}
	}

	if v.GetBool("memory") {
		cfg.Memory = &host.MemoryConfig{InjectSummary: v.GetBool("memory-inject-summary")}
	}

	if ct := v.GetInt("compact-tokens"); ct > 0 {
		cfg.Compaction = &host.CompactionConfig{MaxTokens: ct, KeepRecent: v.GetInt("compact-keep-recent")}
	}

	app, err := host.NewApp(cfg, os.Stdout, os.Stdin, appOpts...)
	if err != nil {
		return err
	}
	defer app.Close()

	if session := v.GetString("session"); session != "" {
		if store == nil {
			return fmt.Errorf("agentchat: --session needs --session-store")
		}
		if err := app.AttachRun(ctx, session); err != nil {
			return err
		}
		fmt.Printf("agentchat: session %s\n", session)
	}

	if tuiMode {
		return runTUI(app, surface)
	}

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
