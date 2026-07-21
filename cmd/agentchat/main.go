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
	"github.com/panyam/mcpkit/core"

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

// resolveEmbedder picks the embedder for semantic memory and its vector
// width. Precedence: the --memory-embed-model flag (explicit override, with
// its own url/key/dim flags), then the config's `embedder` connection role
// (host.BuildEmbedder + the connection's dim). Returns (nil, 0, nil) when
// neither is set, which keeps memory on the in-memory substring store.
func resolveEmbedder(v *viper.Viper, cfg *host.Config, tp core.TracerProvider) (agent.Embedder, int, error) {
	if em := v.GetString("memory-embed-model"); em != "" {
		embedder, err := agent.NewOpenAIEmbedder(agent.OpenAIEmbedderConfig{
			BaseURL:        v.GetString("memory-embed-url"),
			Model:          em,
			APIKey:         os.Getenv(v.GetString("memory-embed-api-key-env")),
			TracerProvider: tp,
		})
		return embedder, v.GetInt("memory-embed-dim"), err
	}
	if cfg.Connections != nil {
		if conn, ok := cfg.Connections.EmbedderConnection(); ok {
			embedder, err := host.BuildEmbedder(conn, tp)
			return embedder, conn.Dim, err
		}
	}
	return nil, 0, nil
}

// buildSemanticMemoryStore picks the semantic MemoryStore backend for
// --memory-embed-model. A postgres --session-store routes to the durable
// pgvector store (ANN top-k in SQL, survives restart) over its own handle,
// with dim as the pgvector column width; every other spec (including empty,
// sqlite, and redis, which have no vector index) uses the in-process
// brute-force store. Both embed client-side through embedder, so recall is
// identical apart from durability and scale.
func buildSemanticMemoryStore(sessionStore string, embedder agent.Embedder, dim int, tp core.TracerProvider) (agent.MemoryStore, error) {
	if strings.HasPrefix(sessionStore, "postgres://") || strings.HasPrefix(sessionStore, "postgresql://") {
		db, err := openGormDB(postgres.Open(sessionStore))
		if err != nil {
			return nil, err
		}
		return gormstore.NewSemanticMemoryStore(db, embedder, gormstore.WithVectorDimensions(dim))
	}
	return agent.NewInMemorySemanticStore(embedder, agent.WithSemanticTracerProvider(tp))
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
	fl.String("active", "", "override the active chat connection at startup (a name in the config's connections; default = the config's active)")
	fl.Bool("persist-config", false, "persist runtime picks (/provider, /approve) to a sibling <config>.local.json overlay, merged over the base config on the next start (needs --config)")
	fl.String("session-store", "", "session persistence backend: memory | sqlite://path.db | redis://host:port | postgres://user:pass@host:port/db (empty = off)")
	fl.String("session", "", "session run ID to create or resume at startup (needs --session-store)")
	fl.String("ui", "auto", "interface: auto (TUI when interactive) | tui (inline) | notebook (alt-screen, foldable cells) | plain")
	fl.Int("notebook-max-lines", 20, "with --ui notebook, max rows the auto-growing prompt expands to")
	fl.Int("context-window", 0, "model context window in tokens; enables a 'N% context left' gauge in the status line (0 = show token counts only)")
	fl.Int("offload-threshold", 0, "offload tool results at/over N bytes to a store, feeding the model a stub + read_tool_result (0 = off; blobs use --session-store's backend)")
	fl.String("offload-dir", "", "store offloaded tool results as files under this directory (no server needed); overrides --session-store for blobs")
	fl.Bool("memory", false, "enable working memory: remember/recall/forget tools the model manages across turns (in-memory store)")
	fl.Bool("memory-inject-summary", false, "with --memory, prepend a summary of the scratchpad before each turn (costs tokens; off = recall on demand)")
	fl.Int("memory-summary-max-items", 0, "with --memory-inject-summary, cap the injected summary to the newest N notes (0 = all)")
	fl.Int("memory-summary-max-chars", 0, "with --memory-inject-summary, cap the injected summary's length in characters (0 = no cap)")
	fl.String("memory-embed-model", "", "with --memory, use semantic recall: embedding model for the memory store (empty = substring match)")
	fl.String("memory-embed-url", "http://localhost:1234/v1", "OpenAI-compatible /embeddings endpoint for --memory-embed-model")
	fl.String("memory-embed-api-key-env", "", "env var holding the embeddings API key")
	fl.Int("memory-embed-dim", 1536, "with --memory-embed-model on a postgres --session-store, the pgvector column width (must match the embedding model)")
	fl.Bool("memory-inject-recall", false, "with --memory, inject notes relevant to each user message before the turn (auto-recall; best with --memory-embed-model)")
	fl.Int("memory-recall-top-k", 0, "with --memory-inject-recall, max relevant notes to inject (0 = default 5)")
	fl.Float64("memory-recall-min-score", 0, "with --memory-inject-recall, drop recalled notes below this relevance score (0 = no floor)")
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
	configPath := v.GetString("config")
	persistConfig := v.GetBool("persist-config") && configPath != ""
	cfg, err := buildConfig(
		configPath,
		persistConfig,
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
	// --active overrides the config's chat connection at startup, so a real
	// provider can be selected by env/flag without editing the config
	// (validated by NewConnectionRegistry inside NewApp).
	if active := v.GetString("active"); active != "" && cfg.Connections != nil {
		cfg.Connections.Active = active
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
	if persistConfig {
		appOpts = append(appOpts, host.WithConfigOverlay(configPath))
	}
	mode := uiMode(v.GetString("ui"))
	var surface *tuiObserver
	var nbSurface *nbObserver
	switch mode {
	case "tui":
		surface = newTUIObserver()
		appOpts = append(appOpts, host.WithObserver(surface))
	case "notebook":
		nbSurface = newNBObserver()
		appOpts = append(appOpts, host.WithObserver(nbSurface))
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
		cfg.Memory = &host.MemoryConfig{
			InjectSummary:   v.GetBool("memory-inject-summary"),
			SummaryMaxItems: v.GetInt("memory-summary-max-items"),
			SummaryMaxChars: v.GetInt("memory-summary-max-chars"),
			InjectRecall:    v.GetBool("memory-inject-recall"),
			RecallTopK:      v.GetInt("memory-recall-top-k"),
			RecallMinScore:  v.GetFloat64("memory-recall-min-score"),
		}
		// A semantic store makes recall similarity-ranked instead of
		// substring. The embedder is resolved from the --memory-embed-model
		// flag (override) or the config's `embedder` connection role; with
		// neither, memory stays the in-memory substring default.
		embedder, dim, err := resolveEmbedder(v, cfg, tp)
		if err != nil {
			return err
		}
		if embedder != nil {
			store, err := buildSemanticMemoryStore(v.GetString("session-store"), embedder, dim, tp)
			if err != nil {
				return err
			}
			appOpts = append(appOpts, host.WithMemoryStore(store))
		}
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

	window := v.GetInt("context-window")
	switch mode {
	case "tui":
		return runTUI(app, surface, window)
	case "notebook":
		return runNotebook(app, nbSurface, v.GetInt("notebook-max-lines"), window)
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
func buildConfig(path string, persist bool, urls []string, baseURL, model, apiKeyEnv, instructions string, maxSteps int) (*host.Config, error) {
	if path != "" {
		if persist {
			// Merge the sibling <config>.local.json overlay (the runtime picks
			// persisted by /provider and /approve) over the base config.
			return host.LoadConfigWithOverlay(path)
		}
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
