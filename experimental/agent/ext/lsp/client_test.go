package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startStub(t *testing.T, root string, s stubScript) *client {
	t.Helper()
	c, err := startClient(context.Background(), stubSpec(t, s), []string{root})
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

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	got := c.diagnostics(filepath.Join(root, "a.go"))
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

	if c.refresh(context.Background(), filepath.Join(root, "a.go"), 200*time.Millisecond) {
		t.Fatal("refresh claimed success from a server that published nothing")
	}
	if got := c.diagnostics(filepath.Join(root, "a.go")); len(got) != 0 {
		t.Fatalf("diagnostics = %+v, want nothing held for a file never published", got)
	}
}

// TestCloseStopsTheServerProcess is the acceptance for host.Extension.Close.
// Without a shutdown seam this subprocess outlives the App that spawned it.
func TestCloseStopsTheServerProcess(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c, err := startClient(context.Background(), stubSpec(t, stubScript{}), []string{root})
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
	c, err := startClient(context.Background(), stubSpec(t, stubScript{IgnoreShutdown: true}), []string{root})
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
	}, []string{root})
	if err == nil {
		t.Fatal("want an error naming the command that could not start")
	}
}

func TestPathFromURIRefusesOutsideEveryRoot(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{})

	if _, ok := c.pathFromURI(pathToURI(root + "/a.go")); !ok {
		t.Fatal("a file inside a root should resolve")
	}
	if _, ok := c.pathFromURI("file:///etc/passwd"); ok {
		t.Fatal("a path outside every root must not resolve to a workspace path")
	}
}

func startStubSettle(t *testing.T, root string, s stubScript, settle time.Duration) *client {
	t.Helper()
	spec := stubSpec(t, s)
	spec.SettleDelay = settle
	c, err := startClient(context.Background(), spec, []string{root})
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

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 10*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	got := c.diagnostics(filepath.Join(root, "a.go"))
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

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("the first refresh should open the file and get diagnostics")
	}
	start := time.Now()
	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("a second refresh over unchanged content should answer from what we hold")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unchanged content took %s, so a didChange was sent and waited on", elapsed)
	}
	if got := c.diagnostics(filepath.Join(root, "a.go")); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want the set from the open", got)
	}
}

// TestRefreshDropsItsWaiterOnTimeout pins that repeated timeouts cannot grow
// the waiter list without bound.
func TestRefreshDropsItsWaiterOnTimeout(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{NoPublish: true})

	if c.refresh(context.Background(), filepath.Join(root, "a.go"), 150*time.Millisecond) {
		t.Fatal("refresh claimed success from a server that published nothing")
	}
	c.mu.Lock()
	left := len(c.waiters[filepath.Join(root, "a.go")])
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d waiter(s) left behind after a timeout", left)
	}
}

func TestServerSpecSettleDelayOverridesTheDefault(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{}, 1234*time.Millisecond)
	if c.settle != 1234*time.Millisecond {
		t.Fatalf("settle = %v, want the spec's value", c.settle)
	}
}

// TestDiagnosticsTimeoutOutlastsTheSettle pins that the two bounds agree. A
// timeout shorter than the settle would cut off the very publication the
// settle exists to wait for.
func TestDiagnosticsTimeoutOutlastsTheSettle(t *testing.T) {
	if DefaultDiagnosticsTimeout <= DefaultSettleDelay {
		t.Fatalf("timeout %v does not outlast settle %v", DefaultDiagnosticsTimeout, DefaultSettleDelay)
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
	if _, err := c.sync(filepath.Join(root, "a.go")); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("refresh gave up on a file the server had not answered for")
	}
	if got := c.diagnostics(filepath.Join(root, "a.go")); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want the server's answer rather than a premature clean", got)
	}
}

// TestRefreshReturnsAtOnceOnRealProblems pins the latency short-circuit. A
// publication with problems in it is actionable, so waiting out a quiet period
// for more of them delays the model for nothing: a later publication can only
// add problems, and the next turn's context stage carries the full set anyway.
func TestRefreshReturnsAtOnceOnRealProblems(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{
		Diagnostics: map[string][]diagnostic{"a.go": {{Message: "undefined: foo"}}},
	}, 5*time.Second)

	start := time.Now()
	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 20*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %s: a non-empty result should not wait out the settle", elapsed)
	}
}

// TestRefreshWaitsOutTheSettleOnAnEmptyResult is the other half. An empty set
// means "clean" or "not computed yet" and nothing distinguishes them, so this
// is the case the quiet period exists for.
func TestRefreshWaitsOutTheSettleOnAnEmptyResult(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{}, 600*time.Millisecond)

	start := time.Now()
	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 20*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("took %s: an empty result must wait for a possible correction", elapsed)
	}
}

// TestBusyGraceIsBounded pins that a busy server extends the wait rather than
// removing the bound. An earlier version waited for the server to fall quiet,
// which measured at the full eight-second timeout on every rust-analyzer write
// because it reports progress almost continuously.
func TestBusyGraceIsBounded(t *testing.T) {
	if busyGrace >= DefaultDiagnosticsTimeout {
		t.Fatalf("busyGrace %v must stay well inside the %v timeout", busyGrace, DefaultDiagnosticsTimeout)
	}
}

// TestSyncSavesSoSaveDrivenServersRecheck pins that a write sends didSave and
// not only didChange.
//
// rust-analyzer publishes nothing at all for a didChange, because its cargo
// check runs on save. Without this, the first edit to a file was checked only
// because opening the document happened to trigger one, and every edit after
// that went unchecked.
func TestSyncSavesSoSaveDrivenServersRecheck(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStub(t, root, stubScript{
		PublishOnSaveOnly: true,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Message: "undefined: foo"}},
		},
	})

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("a save-driven server was never told the file was saved on open")
	}

	// The second pass is the one that matters. The first goes through didOpen,
	// which saves too, so a version that saved only on open would still look
	// correct here. Changing the content forces the didChange path.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.diags[filepath.Join(root, "a.go")] = nil
	c.mu.Unlock()

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 5*time.Second) {
		t.Fatal("a changed file was never reported as saved, so the server never re-checked")
	}
	if got := c.diagnostics(filepath.Join(root, "a.go")); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want the set the save produced", got)
	}
}

// TestBusyServerExtendsTheWait pins that a server reporting itself busy buys
// more patience than the plain settle, so a slow recheck is not cut off and
// reported as clean.
func TestBusyServerExtendsTheWait(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	c := startStubSettle(t, root, stubScript{
		EmptyFirst:        true,
		BusyAroundPublish: true,
		PublishDelayMs:    700,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Message: "undefined: foo"}},
		},
	}, 100*time.Millisecond)

	if !c.refresh(context.Background(), filepath.Join(root, "a.go"), 10*time.Second) {
		t.Fatal("refresh reported no publication")
	}
	if got := c.diagnostics(filepath.Join(root, "a.go")); len(got) != 1 {
		t.Fatalf("diagnostics = %+v: a 100ms settle gave up on a busy server", got)
	}
}
