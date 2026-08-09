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

// TestSpotlightTrustedToolsOptOut pins the config escape hatch.
func TestSpotlightTrustedToolsOptOut(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Spotlight = &SpotlightConfig{TrustedTools: []string{"echo"}}
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
		t.Fatalf("a trusted tool was marked anyway: %q", toolMsg.Text)
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
