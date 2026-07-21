package main

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

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

func TestIsBoundary(t *testing.T) {
	// the streaming turn accumulates live; everything else closes a segment
	if isBoundary(host.HostRunnerEvent) {
		t.Fatal("HostRunnerEvent should stream live, not commit")
	}
	for _, k := range []host.HostEventKind{
		host.HostTurnDone, host.HostTurnFailed, host.HostCommandResult,
		host.HostSessionChanged, host.HostMessage,
	} {
		if !isBoundary(k) {
			t.Fatalf("kind %v should be a commit boundary", k)
		}
	}
}

func TestUpdateLiveThenCommit(t *testing.T) {
	m := newTUIModel(nil, nil, 0)

	// a live segment lands in the pending region, not scrollback
	next, _ := m.Update(liveMsg("thinking…"))
	m = next.(tuiModel)
	if m.pending != "thinking…" {
		t.Fatalf("pending = %q, want the live segment", m.pending)
	}
	if !strings.Contains(m.View(), "thinking…") {
		t.Fatal("live segment should render in the view")
	}

	// a commit clears pending and emits a Println command (to scrollback)
	next, cmd := m.Update(commitMsg("final answer"))
	m = next.(tuiModel)
	if m.pending != "" {
		t.Fatalf("commit should clear pending, got %q", m.pending)
	}
	if cmd == nil {
		t.Fatal("commit with content should return a Println command")
	}
	if strings.Contains(m.View(), "final answer") {
		t.Fatal("committed text belongs in scrollback, not the managed frame")
	}

	// an empty commit is a no-op (no blank line pushed to scrollback)
	if _, cmd := m.Update(commitMsg("")); cmd != nil {
		t.Fatal("empty commit should not push to scrollback")
	}
}

func TestUpdateTurnDoneClearsRunning(t *testing.T) {
	m := newTUIModel(nil, nil, 0)
	m.running = true
	next, _ := m.Update(turnDoneMsg{})
	if next.(tuiModel).running {
		t.Fatal("turnDoneMsg should clear running")
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

func TestKeyMap_WordNavHasCtrlArrows(t *testing.T) {
	// newTUIModel only stores app/surface; nil is fine for inspecting the
	// configured textarea KeyMap.
	m := newTUIModel(nil, nil, 0)
	fwd := m.ta.KeyMap.WordForward.Keys()
	back := m.ta.KeyMap.WordBackward.Keys()
	if !slices.Contains(fwd, "ctrl+right") {
		t.Fatalf("WordForward keys = %v, want ctrl+right (word nav without Meta)", fwd)
	}
	if !slices.Contains(back, "ctrl+left") {
		t.Fatalf("WordBackward keys = %v, want ctrl+left", back)
	}
	// the Meta bindings must survive the augmentation
	if !slices.Contains(fwd, "alt+f") || !slices.Contains(back, "alt+b") {
		t.Fatalf("alt word-nav bindings dropped: fwd=%v back=%v", fwd, back)
	}
}

func TestKeyHelp_ListsBindings(t *testing.T) {
	h := keyHelp()
	for _, want := range []string{"ctrl+← / ctrl+→", "ctrl+a / ctrl+e", "ctrl+w", "ctrl+k / ctrl+u", "Option-as-Meta"} {
		if !strings.Contains(h, want) {
			t.Fatalf("keyHelp() missing %q:\n%s", want, h)
		}
	}
}

func TestKeyMap_CtrlRightMovesByWord(t *testing.T) {
	m := newTUIModel(nil, nil, 0)
	m.ta.SetValue("one two three")
	m.ta.CursorStart()
	if col := m.ta.LineInfo().ColumnOffset; col != 0 {
		t.Fatalf("cursor not at start: %d", col)
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	after := nm.(tuiModel).ta.LineInfo().ColumnOffset
	if after < 3 {
		t.Fatalf("ctrl+right did not advance past the first word: col=%d", after)
	}
}
