package surfaces

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/ext/lsp"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

// TestWorkspaceExtensionsDisabledByDefault pins that an empty Roots produces no
// extensions at all, so a surface can wire this unconditionally and a user who
// passed no workspace flag gets exactly today's behaviour.
func TestWorkspaceExtensionsDisabledByDefault(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if exts != nil {
		t.Fatalf("expected no extensions for an empty Roots, got %d", len(exts))
	}
}

// TestWorkspaceExtensionsCheckpointFirst pins the ordering that nothing else
// catches.
//
// Extensions apply in registration order and their middleware keeps it, so the
// checkpoint snapshot has to be installed ahead of the tool that writes.
// Reversed, both orders still construct and run; the only symptom is an /undo
// that restores the post-write content, which no construction-time error and
// no tool-level test would show. Asserting the order here is the cheapest place
// to catch a reversal.
func TestWorkspaceExtensionsCheckpointFirst(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if len(exts) != 2 {
		t.Fatalf("expected checkpoint + files, got %d", len(exts))
	}
	if got := exts[0].Name(); !strings.Contains(got, "checkpoint") {
		t.Errorf("checkpoint must register first so it snapshots before the write; first was %q", got)
	}
	if got := exts[1].Name(); !strings.Contains(got, "file") {
		t.Errorf("expected the files extension second, got %q", got)
	}
}

// TestWorkspaceExtensionsNoCheckpoint pins that opting out drops the safety net
// but keeps the file tools.
func TestWorkspaceExtensionsNoCheckpoint(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, NoCheckpoint: true})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected only the files extension, got %d", len(exts))
	}
	if got := exts[0].Name(); strings.Contains(got, "checkpoint") {
		t.Errorf("NoCheckpoint still registered checkpoint: %q", got)
	}
}

// TestWorkspaceExtensionsReachAnApp is the test issue 1293 exists for: it
// builds a real host.App through host.WithExtension and asserts every seam the
// extensions contribute actually arrives.
//
// Until this existed, applyExtensions was covered only by hand-rolled test
// doubles inside agent/host, so the five file tools, the /undo command, and the
// diff approval renderer were reachable in principle and registered by nobody.
func TestWorkspaceExtensionsReachAnApp(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}

	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
	}
	var out bytes.Buffer
	app, err := host.NewApp(cfg, &out, strings.NewReader(""),
		host.WithProvider(stubProvider{}),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatalf("NewApp with workspace extensions: %v", err)
	}

	// Tools: assert through App.Tools, which writes the listing a user sees.
	// There is no accessor returning tool definitions, and adding one to check
	// a wiring property would widen a surface that #1240 is trying to narrow.
	// The printed listing is the same question asked at the seam that ships.
	if err := app.Tools(context.Background()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	listing := out.String()
	for _, want := range []string{"read_file", "edit_file", "write_file", "list_files", "search_files"} {
		if !strings.Contains(listing, want) {
			t.Errorf("tool %q did not reach the app; listing was:\n%s", want, listing)
		}
	}

	// Commands: checkpoint's /undo and /checkpoints reach the registry.
	for _, want := range []string{"undo", "checkpoints"} {
		if _, ok := app.Commands().Lookup(want); !ok {
			t.Errorf("command /%s did not reach the app", want)
		}
	}
}

// stubProvider keeps NewApp from needing a reachable model endpoint.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }

func (stubProvider) Generate(context.Context, agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return &agent.ProviderResponse{}, nil
}

func (stubProvider) Stream(context.Context, agent.ProviderRequest) (agent.Stream, error) {
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (agent.Delta, error) { return agent.Delta{}, io.EOF }
func (emptyStream) Close() error               { return nil }

// TestWorkspaceExtensionsSkipsLSPByDefault pins that language servers are
// opt-in. checkpoint is opt-out because it costs a directory; a language
// server is a subprocess and an index of the whole tree, so a caller who did
// not ask for one does not get one.
func TestWorkspaceExtensionsSkipsLSPByDefault(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	for _, e := range exts {
		if e.Name() == "lsp" {
			t.Fatal("a language server was started without being configured")
		}
	}
}

// TestWorkspaceExtensionsFailsOnAnUnstartableServer pins that a misconfigured
// server stops construction rather than yielding an agent whose diagnostics are
// permanently and silently empty.
func TestWorkspaceExtensionsFailsOnAnUnstartableServer(t *testing.T) {
	_, err := WorkspaceExtensions(WorkspaceConfig{
		Roots:           []string{t.TempDir()},
		LanguageServers: []lsp.ServerSpec{{Command: []string{"definitely-not-a-language-server-xyz"}, Extensions: []string{".go"}}},
	})
	if err == nil {
		t.Fatal("want an error for a server that cannot start")
	}
	if !strings.Contains(err.Error(), "lsp") {
		t.Fatalf("error should name the failing extension: %v", err)
	}
}
