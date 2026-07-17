package host

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

// recordSurface captures the UIEvent stream so a test can assert what the
// host emitted, independent of any terminal formatting — the proof that a
// non-terminal surface (web) gets everything it needs.
type recordSurface struct {
	mu  sync.Mutex
	evs []UIEvent
}

func (s *recordSurface) Emit(ev UIEvent) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
}

func (s *recordSurface) kinds() []UIKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UIKind, len(s.evs))
	for i, e := range s.evs {
		out[i] = e.Kind
	}
	return out
}

func (s *recordSurface) has(k UIKind) bool {
	for _, got := range s.kinds() {
		if got == k {
			return true
		}
	}
	return false
}

func TestSurface_TurnEmitsStructuredEvents(t *testing.T) {
	ts := startTestServer(t)
	rec := &recordSurface{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hello"})
	app, err := NewApp(testConfig(ts.URL), nil, strings.NewReader(""),
		WithProvider(stub), WithSurface(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// the turn streamed as runner events and ended with a turn-done
	if !rec.has(UIRunnerEvent) {
		t.Fatalf("no runner events emitted: %v", rec.kinds())
	}
	if !rec.has(UITurnDone) {
		t.Fatalf("no turn-done emitted: %v", rec.kinds())
	}
	// the final event carries the TurnResult, not a formatted string
	last := rec.evs[len(rec.evs)-1]
	if last.Kind != UITurnDone || last.Result == nil || last.Result.Text != "hello" {
		t.Fatalf("turn-done payload = %+v", last)
	}
}

func TestSurface_CommandEmitsUICommand(t *testing.T) {
	ts := startTestServer(t)
	rec := &recordSurface{}
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()
	app, err := NewApp(cfg, nil, strings.NewReader(""),
		WithProviderBuilder(stubBuilder(t)), WithSurface(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// drive a command through the real REPL path
	if err := app.REPL(context.Background(), strings.NewReader("/provider\n/quit\n"), nil); err != nil {
		t.Fatal(err)
	}
	var providers *UIEvent
	for i := range rec.evs {
		if rec.evs[i].Kind == UICommand && rec.evs[i].Command.Kind == CmdProviders {
			providers = &rec.evs[i]
		}
	}
	if providers == nil {
		t.Fatalf("no UICommand/CmdProviders emitted: %v", rec.kinds())
	}
	if providers.Command.ActiveProvider != "local" || len(providers.Command.Providers) != 2 {
		t.Fatalf("provider command payload = %+v", providers.Command)
	}
}

// TestSurface_DefaultRendererStillFormats pins that the default terminal
// path is unchanged: with no WithSurface, output still lands on the writer.
func TestSurface_DefaultRendererStillFormats(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	stub := agent.NewStubProvider(agent.StubTurn{Text: "world"})
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "world") || !strings.Contains(out.String(), "step(s)") {
		t.Fatalf("default renderer did not format the turn:\n%s", out.String())
	}
}
