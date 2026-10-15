package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
)

func runDemo() {
	url := common.ServerURL() + "/mcp"
	wire := common.WireFromArgs()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("agents-discovery-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	opts := []client.ClientOption{client.WithTracerProvider(tp)}
	if opt, ok := wire.ClientOption(); ok {
		opts = append(opts, opt)
	}

	demo := demokit.New("MCP agents: discovery, scoping, per-caller filtering and step-up").
		Dir("agents/discovery").
		Description("Walks through the agent discovery extension (agents-wg#22, implemented in `experimental/ext/agents`). A server advertises agents, each a named group of its own tools with instructions. A host lists them with `agents/list`, opens one with `agents/get`, and runs it locally, calling the scoped tools through the ordinary `tools/call`. Three callers with different scopes show what each one is allowed to see and open.").
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Server", "MCP Server (just serve)"),
		)

	demo.Section("Setup",
		"Start the server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # agents server on :8080",
		"Terminal 2:  just demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"The server accepts three demo bearer tokens in place of an authorization server: `alice-read` (`ops:read`), `alice-deploy` (`ops:read ops:deploy`) and `bob-billing` (`ops:read billing:read`). `examples/fine-grained-auth` shows the same step-up flow against a real one.",
	)

	demo.Section("The roster",
		"| Agent | Tools | Who sees it | Who can open it |",
		"|---|---|---|---|",
		"| `pipeline-agent` | `list_failed_pipelines`, `get_pipeline_log` | everyone | everyone |",
		"| `billing-agent` | `list_invoices` | `billing:read` only | `billing:read` only |",
		"| `deploy-agent` | `list_failed_pipelines`, `deploy_service` | everyone | `ops:deploy` only (step-up) |",
	)

	var alice, bob *caller

	demo.Step("Connect as Alice and check the extension").
		Arrow("Host", "Server", "initialize (Bearer alice-read)").
		DashedArrow("Server", "Host", "capabilities.extensions[\"io.modelcontextprotocol/agents\"] = {listChanged: true}").
		Note("A host must not assume agent discovery unless the server advertises it. `listChanged: true` says the server will announce roster changes.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			alice, err = connect(sc.Ctx, url, "alice-read", opts...)
			if err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
				return
			}
			settings, _ := alice.mcp.ServerExtensionCapability(agents.ExtensionID)
			fmt.Printf("    connected to %s, agents extension: %v, settings: %v\n",
				alice.mcp.ServerInfo.Name, alice.agents.SupportsAgents(), map[string]any(settings))
			return
		})

	demo.Step("List the agents Alice can see").
		Arrow("Host", "Server", "agents/list").
		DashedArrow("Server", "Host", "{agents: [cards], ttlMs: 60000, cacheScope: \"private\"}").
		Note("The roster carries routing fields only, never instructions or tool schemas, so a large roster stays cheap. `billing-agent` is missing because Alice lacks `billing:read`. `cacheScope` is `private` because the answer depends on who asks.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			page, err := alice.agents.ListAgentsPage(sc.Ctx, "")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			for _, card := range page.Agents {
				fmt.Printf("    %-15s %s %v\n", card.Name, card.Description, card.Capabilities)
			}
			fmt.Printf("    ttlMs=%d cacheScope=%s\n", *page.TTLMs, page.CacheScope)
			return
		})

	var pipeline agents.GetAgentResult
	demo.Step("Open pipeline-agent").
		Arrow("Host", "Server", "agents/get {name: \"pipeline-agent\"}").
		DashedArrow("Server", "Host", "{agent, instructions, modelRoutingHint, tools: [...]}").
		Note("`agents/get` returns the instructions, an advisory model hint and the full schemas of the agent's tools. Every tool it returns also appears in `tools/list`, field for field. The host maps the hint onto its own models with `agentsclient.RoleTable`.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			pipeline, err = alice.cache.GetAgent(sc.Ctx, "pipeline-agent")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    instructions: %s\n", pipeline.Instructions)
			model, how := models.Resolve(pipeline.ModelRoutingHint)
			fmt.Printf("    modelRoutingHint: %s -> host model %s (%s)\n", pipeline.ModelRoutingHint.Role, model, how)
			match, err := toolsMatchList(sc.Ctx, alice, pipeline)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			for _, td := range pipeline.Tools {
				fmt.Printf("    tool %-22s identical to tools/list entry: %v\n", td.Name, match[td.Name])
			}
			return
		})

	demo.Step("Run it, through tools/call").
		Arrow("Host", "Server", "tools/call list_failed_pipelines").
		Arrow("Host", "Server", "tools/call get_pipeline_log {id}").
		Note("There is no agent-invocation method. The host runs the agent locally with its own model and calls the scoped tools like any other tool. This step stands in for the model and calls each one once.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			out, err := alice.runAgent(sc.Ctx, pipeline)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			for _, td := range pipeline.Tools {
				fmt.Printf("    %-22s -> %s\n", td.Name, out[td.Name])
			}
			return
		})

	demo.Step("Per-caller filtering: Bob sees a different roster").
		Arrow("Host", "Server", "agents/list (Bearer bob-billing)").
		Arrow("Host", "Server", "agents/get billing-agent (Bearer alice-read)").
		DashedArrow("Server", "Host", "-32602 {reason: \"agent_not_found\"}").
		Note("Bob holds `billing:read`, so his roster includes `billing-agent`. When Alice asks for it by name she gets `agent_not_found`, exactly what a name that was never registered returns, so she cannot probe for agents she is not meant to know about.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			bob, err = connect(sc.Ctx, url, "bob-billing", opts...)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			names, err := bob.rosterNames(sc.Ctx)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Bob's roster: %s\n", strings.Join(names, ", "))
			for _, name := range []string{"billing-agent", "no-such-agent"} {
				_, err := alice.agents.GetAgent(sc.Ctx, name)
				fmt.Printf("    Alice agents/get %-14s agent_not_found=%v (%v)\n", name, agentsclient.IsAgentNotFound(err), err)
			}
			return
		})

	demo.Step("Step-up: Alice may see deploy-agent but not open it yet").
		Arrow("Host", "Server", "agents/get deploy-agent (Bearer alice-read)").
		DashedArrow("Server", "Host", "HTTP 403, WWW-Authenticate: Bearer error=\"insufficient_scope\", scope=\"ops:deploy\"").
		Arrow("Host", "Server", "agents/get deploy-agent (Bearer alice-deploy)").
		DashedArrow("Server", "Host", "{agent, instructions, tools}").
		Note("`deploy-agent` is in Alice's roster, so she is allowed to know it exists, but `agents/get` asks for `ops:deploy`. The server answers with the standard step-up challenge, `agentsclient.StepUpScopes` reads the scopes out of it, and the host retries with a broader token for the same user. The refusal is never cached, so the retry reaches the server.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			_, err := alice.cache.GetAgent(sc.Ctx, "deploy-agent")
			scopes, ok := agentsclient.StepUpScopes(err)
			fmt.Printf("    first try: step-up=%v scopes=%v\n", ok, scopes)
			alice.token.set("alice-deploy")
			detail, err := alice.cache.GetAgent(sc.Ctx, "deploy-agent")
			if err != nil {
				fmt.Printf("    ERROR after step-up: %v\n", err)
				return
			}
			model, how := models.Resolve(detail.ModelRoutingHint)
			fmt.Printf("    after step-up: opened %s, %d tool(s), host model %s (%s)\n", detail.Agent, len(detail.Tools), model, how)
			return
		})

	demo.Step("The roster changes, and the host hears about it").
		Arrow("Host", "Server", "tools/call publish_release_agent").
		DashedArrow("Server", "Host", "notifications/agents/list_changed").
		Arrow("Host", "Server", "agents/list").
		Note("The roster was cached for its 60 s `ttlMs`, so only the notification can make the host see `release-agent` sooner. On the session wire it arrives on the GET stream; on the stateless wire `agentsclient.WatchListChanged` opens a `subscriptions/listen` stream for it. Either way the handler calls `Cache.Invalidate`.").
		Run(func(sc demokit.StepContext) (result *demokit.StepResult) {
			if alice == nil {
				fmt.Println("    skipped: not connected")
				return
			}
			heard := make(chan struct{}, 1)
			stop, err := alice.watch(sc.Ctx, func() {
				select {
				case heard <- struct{}{}:
				default:
				}
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			defer stop()
			if _, err := alice.mcp.ToolCall(sc.Ctx, "publish_release_agent", map[string]any{}); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			select {
			case <-heard:
				fmt.Println("    received notifications/agents/list_changed")
			case <-time.After(3 * time.Second):
				fmt.Println("    no notification within 3s")
			}
			names, err := alice.rosterNames(sc.Ctx)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Alice's roster now: %s\n", strings.Join(names, ", "))
			return
		})

	demo.Section("Where to look in the code",
		"- Server: `agents.Register`, `AgentDef` (including `ScopeChallenge`) and `Config.Visible`, in `experimental/ext/agents/`",
		"- Host helpers: `agentsclient.Cache`, `WatchListChanged`, `RoleTable`, `StepUpScopes`, in `experimental/ext/agents/clients/go/`",
		"- This example's host: `host.go`. Its server: `buildServer` in `main.go`",
		"- Spec: https://github.com/modelcontextprotocol/agents-wg/pull/22",
	)

	common.SetupRenderer(demo)
	demo.Execute()

	for _, k := range []*caller{alice, bob} {
		if k != nil {
			k.close()
		}
	}
}

// toolsMatchList reports, per tool, whether the agents/get schema equals the
// caller's tools/list entry byte for byte.
func toolsMatchList(ctx context.Context, k *caller, detail agents.GetAgentResult) (map[string]bool, error) {
	listed, err := k.mcp.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string][]byte{}
	for _, td := range listed {
		b, _ := json.Marshal(td)
		byName[td.Name] = b
	}
	out := map[string]bool{}
	for _, td := range detail.Tools {
		b, _ := json.Marshal(td)
		out[td.Name] = string(b) == string(byName[td.Name])
	}
	return out, nil
}
