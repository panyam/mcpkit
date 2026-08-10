package host

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

var hostMarkerRe = regexp.MustCompile(`BEGIN_UNTRUSTED_[0-9a-f]{16,}`)

func echoTurns() *agent.StubProvider {
	return agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "echo",
			Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`)),
		}}},
		agent.StubTurn{Text: "done"},
	)
}

// TestSpotlightOffByDefault pins that a config without a spotlight block
// leaves tool output byte-for-byte, so turning the feature on is a deliberate
// act rather than something that silently rewrites every transcript.
func TestSpotlightOffByDefault(t *testing.T) {
	ts := startTestServer(t)
	stub := echoTurns()
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if hostMarkerRe.MatchString(toolMsg.Text) {
		t.Fatalf("output was marked without a spotlight config: %q", toolMsg.Text)
	}
}

// TestSpotlightMarksServerToolOutput is the wiring acceptance: with the config
// block set, what the model reads for a real MCP server tool is fenced.
func TestSpotlightMarksServerToolOutput(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Spotlight = &SpotlightConfig{}
	stub := echoTurns()
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if !hostMarkerRe.MatchString(toolMsg.Text) {
		t.Fatalf("server tool output was not marked: %q", toolMsg.Text)
	}
	if !strings.Contains(toolMsg.Text, "echo: hi") {
		t.Fatalf("marking lost the payload: %q", toolMsg.Text)
	}
}

// TestSpotlightOperatorLabelOptOut pins the config escape hatch.
func TestSpotlightOperatorLabelOptOut(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Spotlight = &SpotlightConfig{Tools: map[string]string{"echo": "operator"}}
	stub := echoTurns()
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if hostMarkerRe.MatchString(toolMsg.Text) {
		t.Fatalf("an operator-labelled tool was marked anyway: %q", toolMsg.Text)
	}
}

// TestSpotlightNonOperatorLabelsStillMark pins that only "operator" opts out.
// A config that labels a tool "server" is describing where output came from,
// not vouching for it, so the fence stays.
func TestSpotlightNonOperatorLabelsStillMark(t *testing.T) {
	for _, label := range []string{"server", "world", "agent", "trusted", ""} {
		ts := startTestServer(t)
		cfg := testConfig(ts.URL)
		cfg.Spotlight = &SpotlightConfig{Tools: map[string]string{"echo": label}}
		stub := echoTurns()
		var out strings.Builder
		app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
		if err != nil {
			t.Fatal(err)
		}
		if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
			app.Close()
			t.Fatal(err)
		}
		toolMsg := stub.Requests()[1].Messages[2]
		if !hostMarkerRe.MatchString(toolMsg.Text) {
			t.Errorf("label %q escaped marking: %q", label, toolMsg.Text)
		}
		app.Close()
	}
}

func TestParseProvenance(t *testing.T) {
	cases := map[string]agent.Provenance{
		"operator":   agent.ProvenanceOperator,
		"  Operator": agent.ProvenanceOperator,
		"OPERATOR":   agent.ProvenanceOperator,
		"server":     agent.ProvenanceServer,
		"agent":      agent.ProvenanceAgent,
		"world":      agent.ProvenanceWorld,
		"":           agent.ProvenanceWorld,
		"trusted":    agent.ProvenanceWorld,
		"oprator":    agent.ProvenanceWorld,
	}
	for in, want := range cases {
		if got := parseProvenance(in); got != want {
			t.Errorf("parseProvenance(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSpotlightDoesNotMarkDeniedCalls pins the ordering host wires: the
// permission gate runs inside spotlighting, so a refused call produces no
// result and the denial reaches the model unfenced.
func TestSpotlightDoesNotMarkDeniedCalls(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Spotlight = &SpotlightConfig{}
	cfg.Approval = &ApprovalConfig{Rules: map[string]string{"echo": "deny"}}
	stub := echoTurns()
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if hostMarkerRe.MatchString(toolMsg.Text) {
		t.Fatalf("a denied call was marked: %q", toolMsg.Text)
	}
	if !strings.Contains(toolMsg.Text, "not permitted") {
		t.Fatalf("expected the denial to reach the model: %q", toolMsg.Text)
	}
}

// classifierFor builds an App's classifier over a set of recorded origins, so
// derivation is testable without standing up every source kind.
func classifierFor(t *testing.T, origins map[string]agent.Provenance, labels map[string]string) func(agent.ToolCallInfo) agent.Provenance {
	t.Helper()
	app := &App{sources: agent.NewMultiSource()}
	for id, p := range origins {
		app.recordOrigin(id, p)
	}
	parsed := map[string]agent.Provenance{}
	for name, label := range labels {
		parsed[name] = parseProvenance(label)
	}
	return app.classifyTool(parsed)
}

func addSource(t *testing.T, m *agent.MultiSource, id string, tools ...string) {
	t.Helper()
	src := agent.NewFuncSource()
	for _, name := range tools {
		if err := src.AddToolFunc(core.ToolDef{Name: name, Description: "d"},
			func(context.Context, map[string]any) (*core.ToolResult, error) { return &core.ToolResult{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Add(id, src); err != nil {
		t.Fatal(err)
	}
}

func infoFor(tool string) agent.ToolCallInfo {
	return agent.ToolCallInfo{Call: agent.ToolCall{Name: tool}}
}

// TestProvenanceDerivedFromSource is the point of the feature: an operator who
// configures nothing still gets meaningful labels, instead of everything
// defaulting to world and the distinction Mark acts on never being used.
func TestProvenanceDerivedFromSource(t *testing.T) {
	app := &App{sources: agent.NewMultiSource()}
	app.recordOrigin("prod-api", agent.ProvenanceServer)
	addSource(t, app.sources, "prod-api", "db_query")
	addSource(t, app.sources, "host", "remember")
	addSource(t, app.sources, "runner-control", "spawn")
	addSource(t, app.sources, "subagent:researcher", "delegate")
	addSource(t, app.sources, "fanout:panel", "broadcast")
	addSource(t, app.sources, "my-extension", "checkpoint_undo")

	classify := app.classifyTool(nil)
	cases := map[string]agent.Provenance{
		"db_query":        agent.ProvenanceServer,
		"remember":        agent.ProvenanceOperator,
		"spawn":           agent.ProvenanceOperator,
		"delegate":        agent.ProvenanceAgent,
		"broadcast":       agent.ProvenanceAgent,
		"checkpoint_undo": agent.ProvenanceWorld,
	}
	for tool, want := range cases {
		if got := classify(infoFor(tool)); got != want {
			t.Errorf("%s derived %q, want %q", tool, got, want)
		}
	}
}

// TestExtensionToolIsNeverDerivedOperator is the safety pin. An extension is
// arbitrary code that may shell out or fetch, so the host has no standing to
// vouch for it. Deriving operator here would silently unfence output the
// mitigation exists to fence.
func TestExtensionToolIsNeverDerivedOperator(t *testing.T) {
	app := &App{sources: agent.NewMultiSource()}
	addSource(t, app.sources, "my-extension", "shell_out")
	if got := app.classifyTool(nil)(infoFor("shell_out")); got == agent.ProvenanceOperator {
		t.Fatal("an extension tool was vouched for by derivation")
	}
}

// TestUnknownToolResolvesToWorld pins the fail-closed direction: a name that
// resolves to nothing gets marked rather than passed through.
func TestUnknownToolResolvesToWorld(t *testing.T) {
	app := &App{sources: agent.NewMultiSource()}
	addSource(t, app.sources, "host", "remember")
	if got := app.classifyTool(nil)(infoFor("never_registered")); got != agent.ProvenanceWorld {
		t.Fatalf("unknown tool classified %q, want world", got)
	}
}

// TestCollidingServersResolveToTheirOwnSource is the disambiguation case:
// two servers advertising one name must not borrow each other's provenance.
func TestCollidingServersResolveToTheirOwnSource(t *testing.T) {
	app := &App{sources: agent.NewMultiSource()}
	app.recordOrigin("vendor-api", agent.ProvenanceServer)
	addSource(t, app.sources, "vendor-api", "search")
	addSource(t, app.sources, "subagent:scout", "search")

	classify := app.classifyTool(nil)
	if got := classify(infoFor("vendor-api/search")); got != agent.ProvenanceServer {
		t.Errorf("qualified server tool = %q, want server", got)
	}
	if got := classify(infoFor("subagent:scout/search")); got != agent.ProvenanceAgent {
		t.Errorf("qualified sub-agent tool = %q, want agent", got)
	}
	// The bare name is ambiguous with no resolver, so no source is claimed
	// and it falls to world rather than to whichever registered first.
	if got := classify(infoFor("search")); got != agent.ProvenanceWorld {
		t.Errorf("ambiguous bare name = %q, want world", got)
	}
}

// TestConfigOverridesDerivation pins that an explicit label wins in both
// directions: vouching for something the host would mark, and marking
// something the host would vouch for.
func TestConfigOverridesDerivation(t *testing.T) {
	app := &App{sources: agent.NewMultiSource()}
	app.recordOrigin("prod-api", agent.ProvenanceServer)
	addSource(t, app.sources, "prod-api", "db_query")
	addSource(t, app.sources, "host", "remember")

	classify := app.classifyTool(map[string]agent.Provenance{
		"db_query": agent.ProvenanceOperator,
		"remember": agent.ProvenanceWorld,
	})
	if got := classify(infoFor("db_query")); got != agent.ProvenanceOperator {
		t.Errorf("config did not override derivation upward: %q", got)
	}
	if got := classify(infoFor("remember")); got != agent.ProvenanceWorld {
		t.Errorf("config did not override derivation downward: %q", got)
	}
}

func TestClassifierWithNoSourcesIsWorld(t *testing.T) {
	if got := classifierFor(t, nil, nil)(infoFor("anything")); got != agent.ProvenanceWorld {
		t.Fatalf("classified %q with no sources, want world", got)
	}
}
