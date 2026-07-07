package client

import (
	"log/slog"
	"runtime/debug"
)

// safeGo runs fn in a new goroutine with panic recovery so a panic in
// library-owned background work (reconnect loops, SSE/stdio read loops,
// connect-with-timeout) cannot crash the host process. A recovered panic is
// logged with its stack and the goroutine exits cleanly. `name` identifies the
// goroutine in the log line (issue 420).
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("mcpkit: recovered panic in client goroutine",
					"goroutine", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
