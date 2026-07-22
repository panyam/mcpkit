package main

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	// the streaming turn + nested sub-agent activity accumulate live; every
	// other event closes a segment
	for _, k := range []host.HostEventKind{host.HostRunnerEvent, host.HostSubAgentEvent} {
		if isBoundary(k) {
			t.Fatalf("kind %v should stream live, not commit", k)
		}
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

func TestOverlayOpenRouteDismiss(t *testing.T) {
	m := newTUIModel(nil, nil, 0)
	ov := newOverlay("sessions", []overlayItem{
		{Label: "s1", Actions: []overlayAction{{Label: "resume", Line: "/resume s1"}}},
		{Label: "s2", Actions: []overlayAction{{Label: "resume", Line: "/resume s2"}}},
	})

	// opening the overlay renders it in the managed frame and blurs the input
	next, _ := m.Update(openOverlayMsg{ov: ov})
	m = next.(tuiModel)
	if m.overlay == nil {
		t.Fatal("openOverlayMsg should set the overlay")
	}
	if !strings.Contains(m.View(), "sessions") {
		t.Fatal("overlay title should render in the view")
	}

	// keys route to the overlay (down moves the selection), not the textarea
	next, _ = m.Update(kmsg("down"))
	m = next.(tuiModel)
	if m.overlay.cursor != 1 {
		t.Fatalf("down should move the overlay cursor, got %d", m.overlay.cursor)
	}

	// esc dismisses and refocuses the input
	next, _ = m.Update(kmsg("esc"))
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatal("esc should dismiss the overlay")
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

func TestRenderKeyHelp_ListsEditingBindings(t *testing.T) {
	h := renderKeyHelp()
	for _, want := range []string{"ctrl+←", "ctrl+a / e", "ctrl+w", "ctrl+k / u", "Option-as-Meta"} {
		if !strings.Contains(h, want) {
			t.Fatalf("renderKeyHelp() missing %q:\n%s", want, h)
		}
	}
}

// blockingProvider holds a turn open inside the model call until release is
// closed, so a test can observe the UI while App.turnMu is held by RunTurn.
type blockingProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingProvider) signalStarted() { p.once.Do(func() { close(p.started) }) }

func (p *blockingProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.signalStarted()
	<-p.release
	return nil, errors.New("blocking provider released")
}

func (p *blockingProvider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	p.signalStarted()
	<-p.release
	return nil, errors.New("blocking provider released")
}

// TestView_DoesNotBlockDuringTurn guards the #1074 regression: the status line
// must render from a cached session, never from App.RunID() (which takes
// turnMu, held by RunTurn for the whole turn). Before the fix, View() blocked
// until the turn finished, freezing the UI on the first message.
func TestView_DoesNotBlockDuringTurn(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	cfg := &host.Config{
		Model: host.ModelConfig{BaseURL: "http://unused", Model: "stub"},
		Connections: &host.ConnectionsConfig{
			Active:      "local",
			Connections: map[string]host.ConnectionConfig{"local": {Type: "lmstudio", Model: "m"}},
		},
	}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return prov, nil }
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	m := newTUIModel(app, nil, 0)

	turnDone := make(chan struct{})
	go func() { _ = app.RunTurn(context.Background(), "hi"); close(turnDone) }()
	<-prov.started // the turn now holds turnMu inside the provider

	rendered := make(chan string, 1)
	go func() { rendered <- m.View() }()
	select {
	case <-rendered: // View returned while the turn is in flight — the fix holds
	case <-time.After(2 * time.Second):
		close(prov.release)
		t.Fatal("View() blocked during a turn — status line is reading RunID() under turnMu")
	}

	close(prov.release)
	<-turnDone
}

// stubMD marks a span so a test can see which blocks took the glamour path
// (prose) versus which passed through verbatim (tool/thinking meta lines).
func stubMD(s string) string { return "MD{" + s + "}" }

// tuiCommit drives the observer through a turn and returns the committed
// segment (the string handed to tea.Println at the boundary).
func tuiCommit(obs *tuiObserver, evs ...host.HostEvent) string {
	var commit string
	for _, ev := range evs {
		for _, m := range obs.fold(ev) {
			if c, ok := m.(commitMsg); ok {
				commit = string(c)
			}
		}
	}
	return commit
}

func TestTUIObserver_GlamoursProseKeepsToolLinesVerbatim(t *testing.T) {
	obs := newTUIObserver()
	obs.renderMD = stubMD

	commit := tuiCommit(obs,
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "before **tool**"}),
		runnerEv(agent.Event{Kind: agent.EventToolBegin, ToolCall: &agent.ToolCall{Name: "greet"}}),
		runnerEv(agent.Event{Kind: agent.EventToolEnd, ToolCall: &agent.ToolCall{Name: "greet"}}),
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "after"}),
		turnDoneEv(),
	)

	// both prose runs went through glamour...
	if !strings.Contains(commit, "MD{before **tool**}") {
		t.Fatalf("prose-before-tool not glamoured:\n%s", commit)
	}
	if !strings.Contains(commit, "MD{after}") {
		t.Fatalf("prose-after-tool not glamoured:\n%s", commit)
	}
	// ...but the tool lines stayed verbatim (not wrapped in MD{})
	if !strings.Contains(commit, "greet") {
		t.Fatalf("tool line dropped from commit:\n%s", commit)
	}
	if strings.Contains(commit, "MD{⚙") || strings.Contains(commit, "MD{  ✓") {
		t.Fatalf("tool line was glamoured (should be verbatim):\n%s", commit)
	}
}

func TestTUIObserver_TextOnlyTurnIsGlamoured(t *testing.T) {
	obs := newTUIObserver()
	obs.renderMD = stubMD
	commit := tuiCommit(obs,
		runnerEv(agent.Event{Kind: agent.EventTextDelta, Text: "# Title"}),
		turnDoneEv(),
	)
	if !strings.Contains(commit, "MD{# Title}") {
		t.Fatalf("plain-prose turn not glamoured at commit:\n%s", commit)
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
