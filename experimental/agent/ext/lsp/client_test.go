package lsp

import (
	"context"
	"testing"
	"time"
)

func startStub(t *testing.T, root string, s stubScript) *client {
	t.Helper()
	c, err := startClient(context.Background(), stubSpec(t, s), root)
	if err != nil {
		t.Fatalf("startClient: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })
	return c
}

func TestClientNegotiatesPositionEncoding(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{Encoding: "utf-8"})
	if c.encoding != "utf-8" {
		t.Fatalf("encoding = %q, want utf-8", c.encoding)
	}
}

// TestClientAcceptsAServerThatDeclinesToNegotiate covers gopls v0.23.0, which
// omits positionEncoding whether offered utf-8 alone or alongside utf-16. Per
// spec that means utf-16, which is what byteColumn then has to undo.
func TestClientAcceptsAServerThatDeclinesToNegotiate(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{})
	if c.encoding != "" {
		t.Fatalf("encoding = %q, want empty (utf-16 by omission)", c.encoding)
	}
}

func TestDiagnosticsArriveFromPublish(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Range: textRange{Start: position{Line: 3, Character: 6}}, Severity: severityError, Message: "undefined: foo"}},
	}})

	if !c.refresh(context.Background(), "a.go", 5*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	got := c.diagnostics("a.go")
	if len(got) != 1 || got[0].Message != "undefined: foo" {
		t.Fatalf("diagnostics = %+v", got)
	}
}

// TestRefreshTimesOutRatherThanReportingStale pins the choice in client.refresh.
// Returning the diagnostics we happen to hold would present problems from
// before the edit as problems the edit caused, sending the model to fix
// something it already fixed.
func TestRefreshTimesOutRatherThanReportingStale(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{
		Diagnostics: map[string][]diagnostic{"a.go": {{Message: "stale"}}},
		NoPublish:   true,
	})

	if c.refresh(context.Background(), "a.go", 200*time.Millisecond) {
		t.Fatal("refresh claimed success from a server that published nothing")
	}
	if got := c.diagnostics("a.go"); len(got) != 0 {
		t.Fatalf("diagnostics = %+v, want nothing held for a file never published", got)
	}
}

// TestCloseStopsTheServerProcess is the acceptance for host.Extension.Close.
// Without a shutdown seam this subprocess outlives the App that spawned it.
func TestCloseStopsTheServerProcess(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c, err := startClient(context.Background(), stubSpec(t, stubScript{}), root)
	if err != nil {
		t.Fatalf("startClient: %v", err)
	}
	if c.cmd.Process == nil {
		t.Fatal("no process was started")
	}

	if err := c.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if c.cmd.ProcessState == nil || !c.cmd.ProcessState.Exited() {
		t.Fatalf("server process did not exit: %v", c.cmd.ProcessState)
	}
}

// TestCloseKillsAServerThatIgnoresShutdown pins the backstop. A host that
// blocks forever on Close is worse than one that leaves an orphan, so the
// polite path has a deadline and the kill follows.
func TestCloseKillsAServerThatIgnoresShutdown(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c, err := startClient(context.Background(), stubSpec(t, stubScript{IgnoreShutdown: true}), root)
	if err != nil {
		t.Fatalf("startClient: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- c.close() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error reporting that the server had to be killed")
		}
		if c.cmd.ProcessState == nil {
			t.Fatal("process was never reaped")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("close hung on a server that ignores shutdown")
	}
}

func TestCloseIsSafeTwice(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{})
	if err := c.close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := c.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestStartClientReportsAMissingBinary(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	_, err := startClient(context.Background(), ServerSpec{
		Command:    []string{"definitely-not-a-language-server-xyz"},
		Extensions: []string{".go"},
	}, root)
	if err == nil {
		t.Fatal("want an error naming the command that could not start")
	}
}

func TestRelFromURIRefusesOutsideTheRoot(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{})

	if _, ok := c.relFromURI(pathToURI(root + "/a.go")); !ok {
		t.Fatal("a file inside the root should resolve")
	}
	if _, ok := c.relFromURI("file:///etc/passwd"); ok {
		t.Fatal("a path outside the root must not resolve to a workspace path")
	}
}
