//go:build lsp_live

// Package lsp's live test drives a real language server.
//
// Everything else in this package drives a stub, which proves the framing, the
// bookkeeping, and the wiring, and proves nothing about whether a real server
// behaves the way the stub was written to. This file closes that gap and is
// tagged out of the default build because it needs a server installed and is
// slower than the rest of the suite by two orders of magnitude.
//
//	go test -tags lsp_live ./experimental/agent/ext/lsp/ -run TestLive -v
//
// LSP_LIVE_SERVER overrides the command; it defaults to gopls.
package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func liveWorkspace(t *testing.T) (string, ServerSpec) {
	t.Helper()
	server := os.Getenv("LSP_LIVE_SERVER")
	if server == "" {
		server = "gopls"
	}
	if _, err := exec.LookPath(server); err != nil {
		t.Skipf("%s is not installed", server)
	}

	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(resolved, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module livetest\n\ngo 1.21\n")
	write("lib.go", "package livetest\n\n// Get returns a number.\nfunc Get() int {\n\treturn 1\n}\n")
	write("use.go", "package livetest\n\nfunc One() int { return Get() }\n\nfunc Two() int { return Get() }\n")

	return resolved, ServerSpec{Command: []string{server}, Extensions: []string{".go"}, LanguageID: "go"}
}

// TestLiveDiagnosticsReportARealCompileError is the claim the stub cannot make:
// a real server, given genuinely broken code, publishes a diagnostic that
// reaches the middleware's report.
func TestLiveDiagnosticsReportARealCompileError(t *testing.T) {
	root, spec := liveWorkspace(t)
	ext, err := New(Config{
		Roots:              []string{root},
		Servers:            []ServerSpec{spec},
		Writes:             []WriteSpec{{Tool: "edit_file", Paths: pathArg}},
		DiagnosticsTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ext.Close() }()

	broken := "package livetest\n\nfunc Three() int { return NotDefinedAnywhere() }\n"
	if err := os.WriteFile(filepath.Join(root, "broken.go"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "broken.go"), okResult("wrote broken.go"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	got := resultText(res)
	t.Logf("server reported:\n%s", got)
	if !strings.Contains(got, "NotDefinedAnywhere") {
		t.Fatalf("a real undefined symbol was not reported: %q", got)
	}
}

// TestLiveNavigationResolvesBySymbolName is the one that would catch a wrong
// assumption about documentSymbol: the stub answers with whatever shape the
// test wrote, while gopls names methods and functions its own way.
func TestLiveNavigationResolvesBySymbolName(t *testing.T) {
	root, spec := liveWorkspace(t)
	ext, err := New(Config{Roots: []string{root}, Servers: []ServerSpec{spec}, DiagnosticsTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ext.Close() }()

	src, err := ext.Tools()
	if err != nil {
		t.Fatal(err)
	}

	// gopls indexes the module before it can answer, so the first request can
	// arrive too early. Retrying is the honest shape of the dependency rather
	// than a flake being papered over.
	var text string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		res, callErr := src.Call(context.Background(), "find_references", map[string]any{"path": "lib.go", "symbol": "Get"})
		if callErr != nil {
			t.Fatalf("Call: %v", callErr)
		}
		text = res.Content[0].Text
		if strings.Contains(text, "use.go") {
			break
		}
		time.Sleep(time.Second)
	}
	t.Logf("references:\n%s", text)
	if !strings.Contains(text, "use.go") {
		t.Fatalf("both uses in use.go should be found, got:\n%s", text)
	}

	res, err := src.Call(context.Background(), "goto_definition", map[string]any{"path": "use.go", "symbol": "One"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	t.Logf("definition:\n%s", res.Content[0].Text)
	if res.IsError {
		t.Fatalf("goto_definition refused: %s", res.Content[0].Text)
	}
}

// TestLiveServerExits pins the seam this PR adds to host.Extension against a
// real subprocess rather than the stub.
func TestLiveServerExits(t *testing.T) {
	root, spec := liveWorkspace(t)
	ext, err := New(Config{Roots: []string{root}, Servers: []ServerSpec{spec}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pid := ext.pool.clients[0].cmd.Process.Pid
	if err := ext.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ext.pool.clients[0].cmd.ProcessState == nil {
		t.Fatalf("pid %d was never reaped", pid)
	}
}
