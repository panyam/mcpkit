// Example: server-declared agents (experimental/ext/agents, agents-wg#22).
//
// Two-process architecture:
//
//	Terminal 1:  just serve         # MCP server on :8080 advertising three agents
//	Terminal 2:  just demo          # demokit walkthrough (or `just demo --tui`)
//
// The server advertises a roster of agents, each a named group of its own
// tools with instructions. A host lists the roster (agents/list), opens one
// agent (agents/get) to get its instructions and scoped tool schemas, and runs
// it locally, calling those tools back through the ordinary tools/call. The
// walkthrough plays that host for three callers with different scopes, so it
// can show per-caller filtering and step-up authorization as well.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

// tokens is the demo's stand-in for an authorization server. Each bearer
// token belongs to a user and grants a fixed scope set. Alice has two, the
// second being what she gets after stepping up. A real deployment validates
// JWTs from an authorization server instead; examples/fine-grained-auth shows
// that half.
var tokens = map[string]struct {
	subject string
	scopes  []string
}{
	"alice-read":   {"alice", []string{"ops:read"}},
	"alice-deploy": {"alice", []string{"ops:read", "ops:deploy"}},
	"bob-billing":  {"bob", []string{"ops:read", "billing:read"}},
}

// tokenAuth validates the demo tokens and exposes their claims.
type tokenAuth struct{}

func (tokenAuth) Validate(r *http.Request) error {
	if _, ok := tokens[bearer(r)]; !ok {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unknown token"}
	}
	return nil
}

func (tokenAuth) Claims(r *http.Request) *core.Claims {
	t := tokens[bearer(r)]
	return &core.Claims{Subject: t.subject, Scopes: t.scopes}
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// The roster. pipeline-agent is open to every caller. billing-agent exists
// only for callers holding billing:read, and anyone else gets agent_not_found,
// the same answer as for a name that was never registered. deploy-agent is
// listed for everyone, but agents/get asks for ops:deploy, which is step-up.
func roster() []agents.AgentDef {
	return []agents.AgentDef{
		{
			Name:             "pipeline-agent",
			Description:      "Investigates CI pipeline failures",
			Capabilities:     []string{"pipeline failures", "flaky tests"},
			Instructions:     "Find failed pipelines, read their logs, and say which commit broke them.",
			ModelRoutingHint: &agents.ModelRoutingHint{Role: agents.ModelRoleSmall},
			Tools:            []string{"list_failed_pipelines", "get_pipeline_log"},
		},
		{
			Name:         "billing-agent",
			Description:  "Answers questions about invoices",
			Capabilities: []string{"invoices"},
			Instructions: "Look up invoices and explain charges.",
			Tools:        []string{"list_invoices"},
		},
		{
			Name:             "deploy-agent",
			Description:      "Ships a service to production",
			Capabilities:     []string{"deployments", "rollbacks"},
			Instructions:     "Check the pipeline is green, then deploy. Never deploy a red build.",
			ModelRoutingHint: &agents.ModelRoutingHint{Role: agents.ModelRoleLarge},
			Tools:            []string{"list_failed_pipelines", "deploy_service"},
			ScopeChallenge:   core.RequireScopes("ops:deploy"),
		},
	}
}

// releaseAgent is published at runtime by the publish_release_agent tool, so
// the walkthrough can show notifications/agents/list_changed.
func releaseAgent() agents.AgentDef {
	return agents.AgentDef{
		Name:         "release-agent",
		Description:  "Writes release notes",
		Capabilities: []string{"release notes"},
		Instructions: "Summarise merged changes since the last tag.",
		Tools:        []string{"list_failed_pipelines"},
	}
}

func visible(ctx core.MethodContext, name string) bool {
	return name != "billing-agent" || core.HasScope(ctx, "billing:read")
}

type pipelineArgs struct {
	ID string `json:"id" jsonschema:"description=pipeline id"`
}

type deployArgs struct {
	Service string `json:"service" jsonschema:"description=service to deploy"`
}

// buildServer registers the tools, then the agents over them, and returns the
// registry so tests can change the roster. Tools come first so Register can
// check every agent's tool names against the catalog. conformance adds the
// diagnostic surface conformance_agents.go describes.
func buildServer(srv *server.Server, conformance bool) *agents.Registry {
	srv.Register(core.TextTool[struct{}]("list_failed_pipelines", "List pipelines that failed in the last day",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "build-1841 (main, red), build-1838 (release/2.4, red)", nil
		}))
	srv.Register(core.TextTool[pipelineArgs]("get_pipeline_log", "Fetch the log of one pipeline",
		func(ctx core.ToolContext, a pipelineArgs) (string, error) {
			return a.ID + ": TestCheckout timed out after 30s", nil
		}))
	srv.Register(core.TextTool[struct{}]("list_invoices", "List this month's invoices",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "INV-0091 $1,240.00, INV-0092 $310.50", nil
		}))
	srv.Register(core.TextTool[deployArgs]("deploy_service", "Deploy a service to production",
		func(ctx core.ToolContext, a deployArgs) (string, error) {
			return "deployed " + a.Service, nil
		}))

	ttl := 60_000
	cfg := agents.Config{
		Server:  srv,
		Agents:  roster(),
		TTLMs:   &ttl,
		Visible: visible,
	}
	if conformance {
		applyConformance(&cfg)
	}
	reg, err := agents.Register(cfg)
	if err != nil {
		log.Printf("[agents-discovery] agents.Register: %v", err)
	}

	srv.Register(core.TextTool[struct{}]("publish_release_agent", "Add release-agent to the roster (demo control)",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			if err := reg.AddAgent(releaseAgent()); err != nil {
				return "", err
			}
			return "release-agent published", nil
		}))
	if conformance {
		registerConformanceControls(srv, reg)
	}
	return reg
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	conformance := flag.Bool("conformance-agents", false, "serve the agents conformance fixture: no auth, one agent per page, and the agents_conformance_* controls")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("agents-discovery"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	// The conformance harness holds no bearer token, so the fixture serves
	// anonymously. Every caller then lacks billing:read, which is what makes
	// billing-agent the hidden agent the suite grades.
	var opts []server.Option
	if !*conformance {
		opts = append(opts, server.WithAuth(tokenAuth{}))
	}
	if err := common.RunServer(common.ServerConfig{
		Name:           "agents-discovery",
		Addr:           *addr,
		TracerProvider: tp,
		Options:        opts,
		Wire:           wire,
		Register:       func(srv *server.Server) { buildServer(srv, *conformance) },
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
