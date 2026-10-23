package server

import (
	"context"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

func TestDispatcherClose_FiresHooksOnce(t *testing.T) {
	root := NewDispatcher(core.ServerInfo{Name: "t", Version: "1"})
	n := 0
	root.closeHooks.add(func(context.Context, string) { n++ })
	d := root.newSession()
	d.sessionID = "s1"
	d.Close()
	d.Close()
	if n != 1 {
		t.Fatalf("hook fired %d times, want 1", n)
	}
}
