package core

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestIsPathAllowed_StaticRootsOnly verifies that when the server sets
// allowed roots via WithAllowedRoots (static) and no client roots are
// present, IsPathAllowed checks against the static list. Paths within
// the static roots are allowed; paths outside are denied. Issue #197.
func TestIsPathAllowed_StaticRootsOnly(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	ctx = SetAllowedRoots(ctx, func() []string { return []string{"/workspace", "/tmp"} })

	if !IsPathAllowed(ctx, "/workspace/src/main.go") {
		t.Error("/workspace/src/main.go should be allowed")
	}
	if !IsPathAllowed(ctx, "/tmp/scratch") {
		t.Error("/tmp/scratch should be allowed")
	}
	if IsPathAllowed(ctx, "/etc/passwd") {
		t.Error("/etc/passwd should be denied")
	}
	if IsPathAllowed(ctx, "/home/user/secrets") {
		t.Error("/home/user/secrets should be denied")
	}
}

// TestIsPathAllowed_NoRootsNoRestriction verifies that when neither
// static nor client roots are configured, all paths are allowed. This is
// the default "trust everything" mode for dev tooling with no sandbox.
func TestIsPathAllowed_NoRootsNoRestriction(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	// No SetAllowedRoots call — allowedRoots is nil.

	if !IsPathAllowed(ctx, "/anything/goes") {
		t.Error("all paths should be allowed when no roots are configured")
	}
}

// TestIsPathAllowed_EmptyRootsDeniesAll verifies that an empty allowed
// roots list (explicitly set to []) denies all paths. This is distinct
// from nil (no restriction): an empty list means "no roots are allowed."
func TestIsPathAllowed_EmptyRootsDeniesAll(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	ctx = SetAllowedRoots(ctx, func() []string { return []string{} })

	if IsPathAllowed(ctx, "/anything") {
		t.Error("should deny all paths when allowed roots is empty (not nil)")
	}
}

// TestIsPathAllowed_NoSessionContextAlwaysAllowed verifies that calling
// IsPathAllowed outside a session context (bare context.Background) returns
// true. Defensive: handlers should be free to call IsPathAllowed without
// branching on whether session state is present.
func TestIsPathAllowed_NoSessionContextAlwaysAllowed(t *testing.T) {
	if !IsPathAllowed(context.Background(), "/any/path") {
		t.Error("should allow all paths when no session context is present")
	}
}

// TestIsPathAllowed_PrefixMatchNotSubstring verifies that path matching
// uses directory-prefix semantics, not naive string prefix. "/workspace"
// should allow "/workspace/x" but NOT "/workspace-other/y".
func TestIsPathAllowed_PrefixMatchNotSubstring(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	ctx = SetAllowedRoots(ctx, func() []string { return []string{"/workspace"} })

	if !IsPathAllowed(ctx, "/workspace/src/main.go") {
		t.Error("/workspace/src/main.go should be allowed")
	}
	if !IsPathAllowed(ctx, "/workspace") {
		t.Error("/workspace itself should be allowed")
	}
	if IsPathAllowed(ctx, "/workspace-other/y") {
		t.Error("/workspace-other/y should be denied (substring match, not dir prefix)")
	}
}

// TestAllowedRoots_ReturnsCurrentSnapshot verifies that AllowedRoots
// returns the current snapshot from the allowed-roots function. This is
// the raw accessor; IsPathAllowed builds on it.
func TestAllowedRoots_ReturnsCurrentSnapshot(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	roots := []string{"/a", "/b"}
	ctx = SetAllowedRoots(ctx, func() []string { return roots })

	got := AllowedRoots(ctx)
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("AllowedRoots = %v, want [/a, /b]", got)
	}
}

// TestAllowedRoots_NilWhenNoRoots verifies that AllowedRoots returns nil
// when no roots function is set (no sandbox).
func TestAllowedRoots_NilWhenNoRoots(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	got := AllowedRoots(ctx)
	if got != nil {
		t.Errorf("AllowedRoots = %v, want nil (no roots configured)", got)
	}
}
