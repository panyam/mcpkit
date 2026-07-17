package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func TestWantTUI(t *testing.T) {
	if !wantTUI("tui") {
		t.Fatal("wantTUI(tui) = false")
	}
	if wantTUI("plain") {
		t.Fatal("wantTUI(plain) = true")
	}
	// auto in `go test` (stdout not a char device) resolves to plain
	if wantTUI("auto") {
		t.Fatal("wantTUI(auto) under test should be false (no TTY)")
	}
}

func modelWithHistory(hist ...string) tuiModel {
	ta := textarea.New()
	ta.SetHeight(3)
	m := tuiModel{ta: ta, history: hist, histIdx: len(hist)}
	return m
}

func TestRecallHistory(t *testing.T) {
	m := modelWithHistory("first", "second")

	if !m.recallHistory(-1) || m.ta.Value() != "second" {
		t.Fatalf("up once = %q, want second", m.ta.Value())
	}
	if !m.recallHistory(-1) || m.ta.Value() != "first" {
		t.Fatalf("up twice = %q, want first", m.ta.Value())
	}
	if m.recallHistory(-1) {
		t.Fatal("up past the oldest should return false")
	}
	if !m.recallHistory(1) || m.ta.Value() != "second" {
		t.Fatalf("down = %q, want second", m.ta.Value())
	}
	// down to the empty draft slot clears the input
	if !m.recallHistory(1) || m.ta.Value() != "" {
		t.Fatalf("down to draft = %q, want empty", m.ta.Value())
	}
}

func TestRecallHistoryEmpty(t *testing.T) {
	m := modelWithHistory()
	if m.recallHistory(-1) {
		t.Fatal("recall with no history should return false (fall through to cursor motion)")
	}
}

func TestCompleteTab(t *testing.T) {
	ts := httptest.NewServer(testutil.NewTestServer().Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	cfg := &host.Config{
		Model:   host.ModelConfig{BaseURL: "http://unused", Model: "stub"},
		Servers: []host.ServerConfig{{ID: "test", URL: ts.URL + "/mcp"}},
		Connections: &host.ConnectionsConfig{
			Active: "local",
			Connections: map[string]host.ConnectionConfig{
				"local": {Type: "lmstudio", Model: "m"},
				"cloud": {Type: "openai", Model: "m", APIKeyEnv: "K"},
			},
		},
	}
	build := func(host.ConnectionConfig) (agent.Provider, error) {
		return agent.NewStubProvider(), nil
	}
	app, err := host.NewApp(cfg, nil, strings.NewReader(""), host.WithProviderBuilder(build))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	m := &tuiModel{app: app, ta: textarea.New()}

	// command-name completion: "/prov" -> "/provider "
	m.ta.SetValue("/prov")
	m.completeTab()
	if m.ta.Value() != "/provider " {
		t.Fatalf("name completion = %q, want '/provider '", m.ta.Value())
	}

	// argument completion: "/provider cl" -> "/provider cloud"
	m.ta.SetValue("/provider cl")
	m.completeTab()
	if m.ta.Value() != "/provider cloud" {
		t.Fatalf("arg completion = %q, want '/provider cloud'", m.ta.Value())
	}

	// no unique match leaves the input unchanged
	m.ta.SetValue("/provider ")
	m.completeTab()
	if m.ta.Value() != "/provider " {
		t.Fatalf("ambiguous arg changed input to %q", m.ta.Value())
	}
}
