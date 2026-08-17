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

func startStubSettle(t *testing.T, root string, s stubScript, settle time.Duration) *client {
	t.Helper()
	spec := stubSpec(t, s)
	spec.SettleDelay = settle
	c, err := startClient(context.Background(), spec, root)
	if err != nil {
		t.Fatalf("startClient: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })
	return c
}

// TestRefreshWaitsPastAProvisionalEmptyPublication is the #1303 regression.
// rust-analyzer publishes an empty set the moment a file changes and its real
// answer about two seconds later. Taking the first publication reported a file
// that does not compile as clean, which is the worst direction to be wrong in.
func TestRefreshWaitsPastAProvisionalEmptyPublication(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{
		EmptyFirst:     true,
		PublishDelayMs: 300,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Range: textRange{Start: position{Line: 4}}, Severity: severityError, Message: "undefined: foo"}},
		},
	}, 2*time.Second)

	if !c.refresh(context.Background(), "a.go", 10*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	got := c.diagnostics("a.go")
	if len(got) != 1 || got[0].Message != "undefined: foo" {
		t.Fatalf("settled on the provisional empty set instead of the real one: %+v", got)
	}
}

// TestRefreshNeedsNoRoundTripWhenContentIsUnchanged covers clangd, which does
// not re-publish for a didChange carrying content it already has. Sending one
// anyway left the caller waiting for a publication that was never coming.
func TestRefreshNeedsNoRoundTripWhenContentIsUnchanged(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{
		PublishOnOpenOnly: true,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Message: "undefined: foo"}},
		},
	})

	if !c.refresh(context.Background(), "a.go", 5*time.Second) {
		t.Fatal("the first refresh should open the file and get diagnostics")
	}
	start := time.Now()
	if !c.refresh(context.Background(), "a.go", 5*time.Second) {
		t.Fatal("a second refresh over unchanged content should answer from what we hold")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unchanged content took %s, so a didChange was sent and waited on", elapsed)
	}
	if got := c.diagnostics("a.go"); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want the set from the open", got)
	}
}

// TestRefreshDropsItsWaiterOnTimeout pins that repeated timeouts cannot grow
// the waiter list without bound.
func TestRefreshDropsItsWaiterOnTimeout(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{NoPublish: true})

	if c.refresh(context.Background(), "a.go", 150*time.Millisecond) {
		t.Fatal("refresh claimed success from a server that published nothing")
	}
	c.mu.Lock()
	left := len(c.waiters["a.go"])
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d waiter(s) left behind after a timeout", left)
	}
}

// TestSettleForKnownTwoPhaseServer pins the quirk table. Nothing in the
// protocol lets a server say an answer was provisional, so the delay has to
// come from knowing which servers do it.
func TestSettleForKnownTwoPhaseServer(t *testing.T) {
	if settleFor("rust-analyzer") <= DefaultSettleDelay {
		t.Fatal("rust-analyzer needs a longer settle than the default, or the empty publication wins")
	}
	for _, name := range []string{"gopls", "clangd", ""} {
		if got := settleFor(name); got != DefaultSettleDelay {
			t.Fatalf("settleFor(%q) = %v, want the default", name, got)
		}
	}
}

func TestServerSpecSettleDelayOverridesTheDefault(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{}, 1234*time.Millisecond)
	if c.settle != 1234*time.Millisecond {
		t.Fatalf("settle = %v, want the spec's value", c.settle)
	}
}

// TestDiagnosticsTimeoutOutlastsTheLongestSettle pins that the two bounds
// agree. A timeout shorter than the settle would cut off the very publication
// the settle exists to wait for.
func TestDiagnosticsTimeoutOutlastsTheLongestSettle(t *testing.T) {
	if DefaultDiagnosticsTimeout <= settleFor("rust-analyzer") {
		t.Fatalf("timeout %v does not outlast settle %v", DefaultDiagnosticsTimeout, settleFor("rust-analyzer"))
	}
}

// TestRefreshWaitsWhenTheServerHasNotAnsweredYet guards the narrow case that
// makes the unchanged-content shortcut safe.
//
// A navigation call syncs the file, so by the time a write is re-checked the
// content can already be with the server while the server has said nothing
// about it. Skipping the wait on unchanged content alone reported the file as
// clean before anything had looked at it, which is the same false all-clear
// this change exists to remove.
func TestRefreshWaitsWhenTheServerHasNotAnsweredYet(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{
		PublishDelayMs: 400,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Message: "undefined: foo"}},
		},
	})

	// Open the document without waiting, the way a navigation call does.
	if _, err := c.sync("a.go"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if !c.refresh(context.Background(), "a.go", 5*time.Second) {
		t.Fatal("refresh gave up on a file the server had not answered for")
	}
	if got := c.diagnostics("a.go"); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want the server's answer rather than a premature clean", got)
	}
}
