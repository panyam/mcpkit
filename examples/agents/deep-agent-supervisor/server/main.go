// Command server is the deep-agent-supervisor demo's MCP server. It advertises
// a small roster of SPECIALIST AGENTS via the experimental/ext/agents discovery
// primitive (SEP-2640-adjacent, epic 1142) instead of exposing a flat
// tools/list of every specialist's schemas.
//
// Three agents, mapped to the agents-wg#20 §6 stress test:
//
//	research   a deep-research specialist (web_search / fetch_page / summarize)
//	workflow   a CI/CD operations specialist (list_pipelines / run_pipeline / pipeline_status)
//	insights   an analytics specialist (query_metrics / detect_anomaly)
//
// A supervisor host (cmd/agentchat, via deep-agent-supervisor.json) discovers
// the roster with agents/list — three routing tuples, NO tool schemas — and
// resolves a specialist with agents/get only when it decides to delegate. That
// is the progressive-disclosure win: the supervisor's context stays small.
//
// Every tool here is a deterministic stub so the demo runs without external
// services; only the supervisor's own model needs a provider. The server wires
// a SEP-414 TracerProvider (issue 1145) into both server.WithTracerProvider and
// agents.Config.TracerProvider, so agents.list / agents.get discovery spans and
// the dispatch spans land in your collector alongside the host-side
// agents.resolve + child-Runner spans.
//
// Streamable HTTP on :8795 (kitchen-sink owns 8788-8791, playground 8787).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
)

const addr = ":8795"

func main() {
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.Parse()

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("deep-agent-supervisor"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	srv, _, err := buildServer(tp)
	if err != nil {
		log.Fatalf("buildServer: %v", err)
	}

	log.Printf("deep-agent-supervisor roster server listening on http://localhost%s/mcp", addr)
	if err := srv.Run(addr); err != nil {
		log.Fatal(err)
	}
}

// buildServer registers the specialists' scoped tools and advertises the
// three-agent roster via agents.Register, wiring tp (which may be
// core.NoopTracerProvider{}) into both the server dispatch spans and the
// agents extension's discovery spans. It is factored out of main so the roster
// wiring is testable without booting a listener.
func buildServer(tp core.TracerProvider) (*server.Server, *agents.Registry, error) {
	srv := server.NewServer(
		core.ServerInfo{Name: "deep-agent-supervisor-roster", Version: "0.1.0"},
		server.WithToolTimeout(30*time.Second),
		server.WithTracerProvider(tp),
	)
	registerTools(srv)

	reg, err := agents.Register(agents.Config{
		Server:         srv,
		Agents:         roster(),
		TracerProvider: tp,
	})
	return srv, reg, err
}

// roster is the advertised set of specialists. Each AgentDef carries the
// routing summary (description / capabilities / example tasks) the supervisor
// sees in agents/list, plus the instructions + scoped Tools returned only by
// agents/get. Tools names match the handlers registerTools installs, so a
// resolved specialist's tool call loops back to this server.
func roster() []agents.AgentDef {
	return []agents.AgentDef{
		{
			AgentID:      "research",
			Description:  "Deep-research specialist: searches, reads, and synthesizes sources into a short cited briefing.",
			Capabilities: []string{"web search", "source synthesis", "cited briefings"},
			ExampleTasks: []string{"Research the tradeoffs of approach X", "Find and summarize recent work on Y"},
			DelegateTool: "invoke_research",
			SkillURI:     "skill://research/SKILL.md",
			Instructions: "You are a deep-research specialist. Use web_search to find candidate sources, " +
				"fetch_page to read the two or three most relevant, and summarize to produce a concise " +
				"cited briefing. Prefer a few strong sources over many weak ones. Always cite what you used.",
			Tools: []core.ToolDef{
				toolDef("web_search", "Search the web for sources on a query", "query"),
				toolDef("fetch_page", "Fetch the text of a page by url", "url"),
				toolDef("summarize", "Summarize text into a short briefing", "text"),
			},
		},
		{
			AgentID:      "workflow",
			Description:  "Operations specialist: inspects and runs CI/CD pipelines, approval-aware.",
			Capabilities: []string{"pipeline catalog", "run execution", "status checks"},
			ExampleTasks: []string{"List failing pipelines", "Run the deploy pipeline for service X"},
			DelegateTool: "invoke_workflow",
			TasksEnabled: true,
			Instructions: "You operate CI/CD pipelines. Prefer read-only inspection (list_pipelines, " +
				"pipeline_status) before you run anything, and say what you would run before running it.",
			Tools: []core.ToolDef{
				toolDef("list_pipelines", "List the known pipelines and their last status", ""),
				toolDef("run_pipeline", "Trigger a pipeline run by name", "name"),
				toolDef("pipeline_status", "Get the current status of a pipeline by name", "name"),
			},
		},
		{
			AgentID:      "insights",
			Description:  "Analytics specialist: queries service metrics and flags anomalies.",
			Capabilities: []string{"metric queries", "anomaly detection"},
			ExampleTasks: []string{"What's the p99 latency trend?", "Any anomalies in error rate today?"},
			DelegateTool: "invoke_insights",
			Instructions: "You are an analytics specialist. Query metrics with query_metrics and call out " +
				"anomalies with detect_anomaly, each with a one-line plain-language explanation.",
			Tools: []core.ToolDef{
				toolDef("query_metrics", "Query a named metric over a time window", "metric"),
				toolDef("detect_anomaly", "Check a named metric for anomalies", "metric"),
			},
		},
	}
}

// toolDef builds the scoped ToolDef a specialist advertises to its own model.
// The schema is a single required string argument (arg == "" means no
// arguments), which matches the deterministic handlers below and keeps the
// demo's scoped schemas readable. The real dispatch goes to the server's
// registered handler of the same name.
func toolDef(name, desc, arg string) core.ToolDef {
	schema := map[string]any{"type": "object"}
	if arg != "" {
		schema["properties"] = map[string]any{arg: map[string]any{"type": "string"}}
		schema["required"] = []string{arg}
	}
	return core.ToolDef{Name: name, Description: desc, InputSchema: schema}
}

type queryInput struct {
	Query string `json:"query" jsonschema:"description=What to search for,required"`
}
type urlInput struct {
	URL string `json:"url" jsonschema:"description=The page url to fetch,required"`
}
type textInput struct {
	Text string `json:"text" jsonschema:"description=The text to summarize,required"`
}
type nameInput struct {
	Name string `json:"name" jsonschema:"description=The pipeline name,required"`
}
type metricInput struct {
	Metric string `json:"metric" jsonschema:"description=The metric name,required"`
}
type noInput struct{}

// registerTools installs the deterministic stub handlers for every scoped tool
// the roster references. A specialist resolved via agents/get calls these by
// name; the bridge loops the call back here (the advertising server).
func registerTools(srv *server.Server) {
	srv.Register(core.TextTool[queryInput]("web_search", "Search the web for sources on a query",
		func(ctx core.ToolContext, in queryInput) (string, error) {
			return fmt.Sprintf("Top sources for %q:\n1. https://example.org/a — overview of %s\n2. https://example.org/b — a critical take\n3. https://example.org/c — recent data", in.Query, in.Query), nil
		}))
	srv.Register(core.TextTool[urlInput]("fetch_page", "Fetch the text of a page by url",
		func(ctx core.ToolContext, in urlInput) (string, error) {
			return fmt.Sprintf("[fetched %s]\nThe page argues three points, backs them with a small study, and notes two open questions.", in.URL), nil
		}))
	srv.Register(core.TextTool[textInput]("summarize", "Summarize text into a short briefing",
		func(ctx core.ToolContext, in textInput) (string, error) {
			n := len(strings.Fields(in.Text))
			return fmt.Sprintf("Briefing (%d words in): the sources agree on the main claim and differ on scope; the strongest evidence is the recent data point.", n), nil
		}))

	srv.Register(core.TextTool[noInput]("list_pipelines", "List the known pipelines and their last status",
		func(ctx core.ToolContext, _ noInput) (string, error) {
			return "deploy-web (green)\ndeploy-api (red: last run failed at test stage)\nnightly-batch (green)", nil
		}))
	srv.Register(core.TextTool[nameInput]("run_pipeline", "Trigger a pipeline run by name",
		func(ctx core.ToolContext, in nameInput) (string, error) {
			return fmt.Sprintf("triggered %s: run #421 queued (this is a demo stub; nothing ran)", in.Name), nil
		}))
	srv.Register(core.TextTool[nameInput]("pipeline_status", "Get the current status of a pipeline by name",
		func(ctx core.ToolContext, in nameInput) (string, error) {
			return fmt.Sprintf("%s: last run #420 failed at the test stage (3 flaky tests)", in.Name), nil
		}))

	srv.Register(core.TextTool[metricInput]("query_metrics", "Query a named metric over a time window",
		func(ctx core.ToolContext, in metricInput) (string, error) {
			return fmt.Sprintf("%s over 24h: p50=120ms p95=340ms p99=910ms (up 18%% vs yesterday)", in.Metric), nil
		}))
	srv.Register(core.TextTool[metricInput]("detect_anomaly", "Check a named metric for anomalies",
		func(ctx core.ToolContext, in metricInput) (string, error) {
			return fmt.Sprintf("anomaly in %s: a 3x spike at 14:05 UTC, correlated with the deploy-api failure", in.Metric), nil
		}))
}
