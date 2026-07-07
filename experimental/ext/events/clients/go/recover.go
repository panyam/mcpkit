package eventsclient

import (
	"log/slog"
	"runtime/debug"
)

// safeGo runs fn in a new goroutine with panic recovery so a panic in the
// client's background loops (subscription refresh, stream call) cannot crash
// the host process. A recovered panic is logged with its stack and the
// goroutine exits cleanly (issue 420).
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("mcpkit/events-client: recovered panic in background goroutine",
					"goroutine", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
