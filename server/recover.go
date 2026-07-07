package server

import (
	"log/slog"
	"runtime/debug"
)

// As a library, mcpkit does not own the host process. A panic in a
// library-spawned goroutine — a user tool/resource/prompt handler, middleware,
// or a runtime error like a nil dereference — would otherwise crash the
// consumer's whole address space: their HTTP server, metrics exporters,
// graceful-shutdown hooks, and any other in-flight work. This file centralizes
// the recovery seam so every library-owned goroutine and the synchronous
// dispatch path degrade to a logged error instead of a process abort (issue 420).

// safeGo runs fn in a new goroutine with panic recovery. A recovered panic is
// logged with its stack and the goroutine exits cleanly; it never propagates to
// crash the host. Use this for every library-owned `go ...` site. `name`
// identifies the goroutine in the log line.
func safeGo(name string, fn func()) {
	go func() {
		defer recoverGoroutine(name)
		fn()
	}()
}

// recoverGoroutine is the deferred recovery body for a background goroutine.
// Exposed as a helper so goroutines that need their own `defer` (e.g. to also
// run cleanup) can reuse the same logging shape.
func recoverGoroutine(name string) {
	if r := recover(); r != nil {
		slog.Error("mcpkit: recovered panic in background goroutine",
			"goroutine", name, "panic", r, "stack", string(debug.Stack()))
	}
}
