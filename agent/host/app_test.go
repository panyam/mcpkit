package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(testutil.NewTestServer().Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

func testConfig(url string) *Config {
	return &Config{
		Model:   ModelConfig{BaseURL: "http://unused", Model: "stub"},
		Servers: []ServerConfig{{ID: "test", URL: url + "/mcp"}},
	}
}

// TestAppBootsWithDownOptionalServer is the graceful-degrade acceptance: a down
// optional server must not fail boot; the reachable server is ready, the down
// one is not, and /servers lists both (docs/AGENT_SERVER_STATE.md).
func TestAppBootsWithDownOptionalServer(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Servers = append(cfg.Servers, ServerConfig{ID: "down", URL: "http://127.0.0.1:1/mcp"})

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatalf("boot must not fail on a down optional server: %v", err)
	}
	defer app.Close()

	if s, _ := app.group.State("test"); s != client.StateReady {
		t.Fatalf("reachable server state = %v, want ready", s)
	}
	if s, _ := app.group.State("down"); s == client.StateReady {
		t.Fatalf("down server should not be ready, got %v", s)
	}
	res, err := app.Dispatch(context.Background(), "/servers")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Servers) != 2 {
		t.Fatalf("/servers = %d entries, want 2", len(res.Servers))
	}
}

// TestAppRequiredServerReadyAfterBoot verifies a required server is connected by
// the time NewApp returns (WaitRequired blocked for it).
func TestAppRequiredServerReadyAfterBoot(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Servers[0].Required = true

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatalf("boot with a reachable required server: %v", err)
	}
	defer app.Close()
	if s, _ := app.group.State("test"); s != client.StateReady {
		t.Fatalf("required server must be ready after boot, got %v", s)
	}
}

func TestAppTranscriptEndToEnd(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`))}}},
		agent.StubTurn{Text: "The server said: echo: hi"},
	)

	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "say hi via echo"); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{"⚙ echo(", `"message":"hi"`, "✓ echo: echo: hi", "The server said: echo: hi", "— 2 step(s)"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}

func TestAppHistoryThreadsAcrossTurns(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "first answer"},
		agent.StubTurn{Text: "second answer"},
	)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	app.RunTurn(context.Background(), "one")
	app.RunTurn(context.Background(), "two")

	second := stub.Requests()[1].Messages
	if len(second) != 3 || second[0].Text != "one" || second[1].Text != "first answer" || second[2].Text != "two" {
		t.Fatalf("history threading broken: %+v", second)
	}
}

func TestAppFailedTurnRollsBackHistory(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider() // exhausted immediately
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "doomed"); err == nil {
		t.Fatal("want provider exhaustion error")
	}
	if len(app.history) != 0 {
		t.Fatalf("failed turn must roll back history, got %+v", app.history)
	}
	if !strings.Contains(out.String(), "error:") {
		t.Fatalf("transcript missing error line:\n%s", out.String())
	}
}

func TestAppElicitationThroughScriptedStdin(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "elicit-srv", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "ask", Description: "asks", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			res, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "Pick a color",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"color":{"type":"string","enum":["red","green","blue"]}},"required":["color"]}`),
			})
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			if res.Action != "accept" {
				return core.TextResult("no color chosen"), nil
			}
			return core.TextResult("color=" + res.Content["color"].(string)), nil
		},
	)
	hts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(hts.Close)

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "ask", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "you picked green"},
	)

	var out strings.Builder
	stdin := strings.NewReader("2\n")
	app, err := NewApp(testConfig(hts.URL), &out, stdin, WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "ask me"); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{"? Pick a color", "2) green", "✓ ask: color=green", "you picked green"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}

func TestAppAllowlistIsEnforced(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Servers[0].Allow = []string{"echo"}

	stub := agent.NewStubProvider(agent.StubTurn{Text: "done"})
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	tools, err := app.sources.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		names := make([]string, len(tools))
		for i, d := range tools {
			names[i] = d.Name
		}
		t.Fatalf("allowlist must filter to [echo], got %v", names)
	}
}

func TestREPLCommandsAndQuit(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hello there"})
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	input := strings.NewReader("hi\n/tools\n/quit\n")
	if err := app.REPL(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{"hello there", "echo", "— 2 tool(s)"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}

func TestAppApprovalDenyRuleBlocksTool(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Approval = &ApprovalConfig{Rules: map[string]string{"echo": "deny"}}

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`))}}},
		agent.StubTurn{Text: "I was not allowed to echo."},
	)
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	if !strings.Contains(transcript, "⃠ echo not permitted") {
		t.Fatalf("transcript missing denial line:\n%s", transcript)
	}
	if strings.Contains(transcript, "✓ echo:") {
		t.Fatalf("denied tool must not run:\n%s", transcript)
	}
	// The model is told and the turn continues to its text answer.
	toolMsg := stub.Requests()[1].Messages[2]
	if toolMsg.Role != agent.RoleTool || !strings.Contains(toolMsg.Text, "not permitted") {
		t.Fatalf("model-visible denial missing: %+v", toolMsg)
	}
	if !strings.Contains(transcript, "I was not allowed to echo.") {
		t.Fatalf("turn should continue after denial:\n%s", transcript)
	}
}

func TestAppApprovalAskApproves(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Approval = &ApprovalConfig{Mode: "ask"}

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`))}}},
		agent.StubTurn{Text: "The server said: echo: hi"},
	)
	var out strings.Builder
	// The approval prompt is a boolean "confirm" elicitation; "y" accepts it.
	app, err := NewApp(cfg, &out, strings.NewReader("y\n"), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{`Allow tool call "echo"`, "✓ echo: echo: hi", "The server said: echo: hi"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}

func TestAppApprovalRuntimeToggle(t *testing.T) {
	ts := startTestServer(t)

	// With a policy configured, /approve reports and changes the mode.
	cfg := testConfig(ts.URL)
	cfg.Approval = &ApprovalConfig{Mode: "ask"}
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "x"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if res, err := app.Dispatch(context.Background(), "/approve allow"); err != nil {
		t.Fatal(err)
	} else {
		app.emit(HostEvent{Kind: HostCommandResult, Command: res})
	}
	if app.approval.DefaultMode() != agent.ModeAlwaysAllow {
		t.Fatalf("runtime toggle did not take: %v", app.approval.DefaultMode())
	}
	if !strings.Contains(out.String(), "approval: allow") {
		t.Fatalf("toggle should render new mode:\n%s", out.String())
	}

	// Without a policy, /approve is a no-op that reports the gate is off.
	var out2 strings.Builder
	app2, err := NewApp(testConfig(ts.URL), &out2, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "x"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app2.Close()
	if res, err := app2.Dispatch(context.Background(), "/approve allow"); err != nil {
		t.Fatal(err)
	} else {
		app2.emit(HostEvent{Kind: HostCommandResult, Command: res})
	}
	if !strings.Contains(out2.String(), "approval: off") {
		t.Fatalf("no-policy toggle should say off:\n%s", out2.String())
	}
}

func TestConfigLoadingAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	os.WriteFile(path, []byte(`{
		"model": {"baseUrl": "http://localhost:1234/v1", "model": "m", "apiKeyEnv": "AGENTCHAT_TEST_KEY"},
		"servers": [{"id": "a", "url": "http://x/mcp", "allow": ["t1"]}]
	}`), 0o644)

	t.Setenv("AGENTCHAT_TEST_KEY", "sekrit")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey() != "sekrit" {
		t.Fatalf("env indirection broken: %q", cfg.APIKey())
	}
	if cfg.Servers[0].Allow[0] != "t1" {
		t.Fatalf("allow parse: %+v", cfg.Servers[0])
	}

	bad := &Config{Model: ModelConfig{BaseURL: "x", Model: "m"}, Servers: []ServerConfig{{ID: "dup", URL: "u"}, {ID: "dup", URL: "v"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("want duplicate-id validation error")
	}
}

func TestTerminalElicitationDeclineAndForm(t *testing.T) {
	ui := terminalElicitationUI(bufio.NewReader(strings.NewReader("/d\n")), &strings.Builder{})
	res, err := ui(context.Background(), core.ElicitationRequest{
		Message:         "share location?",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
	})
	if err != nil || res.Action != "decline" {
		t.Fatalf("decline path: %+v %v", res, err)
	}

	// Properties prompt in sorted order: admin, age, nickname. Script:
	// admin=y; age blank (required re-prompt) then 42; nickname blank
	// (optional, omitted).
	var out strings.Builder
	ui = terminalElicitationUI(bufio.NewReader(strings.NewReader("y\n\n42\n\n")), &out)
	res, err = ui(context.Background(), core.ElicitationRequest{
		Message: "profile",
		RequestedSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"age":{"type":"integer"},"admin":{"type":"boolean"},"nickname":{"type":"string"}},
			"required":["age"]
		}`),
	})
	if err != nil || res.Action != "accept" {
		t.Fatalf("form path: %+v %v", res, err)
	}
	if res.Content["age"] != 42 || res.Content["admin"] != true {
		t.Fatalf("form content: %+v", res.Content)
	}
	if _, present := res.Content["nickname"]; present {
		t.Fatalf("optional empty field must be omitted: %+v", res.Content)
	}
	if !strings.Contains(out.String(), "(required)") {
		t.Fatalf("required re-prompt missing:\n%s", out.String())
	}
}

func startSkillsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := testutil.NewTestServer()
	p, err := skills.NewProvider(skills.WithDirectory("testdata/skills"))
	if err != nil {
		t.Fatal(err)
	}
	p.RegisterWith(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

func TestAppInjectsVerifiedSkills(t *testing.T) {
	ts := startSkillsServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	var out strings.Builder
	cfg := testConfig(ts.URL)
	cfg.Instructions = "base prompt"
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	instr := stub.Requests()[0].Instructions
	for _, want := range []string{"base prompt", "## Skills", "### Skill: brevity", "exactly one short sentence"} {
		if !strings.Contains(instr, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instr)
		}
	}
	if !strings.Contains(out.String(), "skills: 1 loaded from test") {
		t.Fatalf("transcript missing skills line:\n%s", out.String())
	}
}

func TestAppSkillsOptOut(t *testing.T) {
	ts := startSkillsServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	var out strings.Builder
	off := false
	cfg := testConfig(ts.URL)
	cfg.Instructions = "base prompt"
	cfg.Servers[0].Skills = &off
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	app.RunTurn(context.Background(), "hello")
	if strings.Contains(stub.Requests()[0].Instructions, "## Skills") {
		t.Fatalf("opt-out must suppress injection:\n%s", stub.Requests()[0].Instructions)
	}
}

func TestAppSkillLessServerIsSilent(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.RunTurn(context.Background(), "hello")
	if strings.Contains(out.String(), "skills:") {
		t.Fatalf("no-skills server must stay silent:\n%s", out.String())
	}
}

func TestAppFailoverToBackupEndToEnd(t *testing.T) {
	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"backup says hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(backupSrv.Close)

	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Model.Backup = &BackupModelConfig{BaseURL: backupSrv.URL, Model: "backup-model"}

	var logBuf bytes.Buffer
	exhausted := agent.NewStubProvider() // errors on first call: a clean primary failure
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProvider(exhausted),
		WithLogger(slog.New(slog.NewTextHandler(&logBuf, nil))),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("turn must succeed via backup: %v", err)
	}
	if !strings.Contains(out.String(), "backup says hi") {
		t.Fatalf("transcript missing backup output:\n%s", out.String())
	}
	if !strings.Contains(logBuf.String(), "routing to backup") {
		t.Fatalf("failover must be logged via slog:\n%s", logBuf.String())
	}

	app.emit(HostEvent{Kind: HostCommandResult, Command: CmdResult{Kind: CmdHealth, Failover: app.failover}})
	if !strings.Contains(out.String(), "active=backup") {
		t.Fatalf("/health must reflect the bench:\n%s", out.String())
	}
}

func TestAppHealthWithoutFailover(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "x"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.emit(HostEvent{Kind: HostCommandResult, Command: CmdResult{Kind: CmdHealth, Failover: app.failover}})
	if !strings.Contains(out.String(), "no failover configured") {
		t.Fatalf("health line missing:\n%s", out.String())
	}
}
