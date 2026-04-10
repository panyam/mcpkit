package server_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
)

// TestRootsNotificationHandled verifies that the server handles
// notifications/roots/list_changed without error. This notification
// is sent by clients when their available filesystem roots change.
func TestRootsNotificationHandled(t *testing.T) {
	srv := testutil.NewTestServer()
	testutil.InitHandshake(srv)

	// Send the notification — should not error or panic
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})
	assert.Nil(t, resp, "notification should not return a response")
}

// TestRootsCallbackFired verifies that WithOnRootsChanged callback is
// invoked when the client sends notifications/roots/list_changed.
func TestRootsCallbackFired(t *testing.T) {
	var callbackFired atomic.Bool

	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithOnRootsChanged(func(roots []core.Root) {
			callbackFired.Store(true)
		}),
	)

	// Register a tool so InitHandshake works
	srv.RegisterTool(
		core.ToolDef{Name: "noop", Description: "noop", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	testutil.InitHandshake(srv)

	srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})

	assert.True(t, callbackFired.Load(), "OnRootsChanged callback should have fired")
}

// TestRootsNotificationBeforeInit verifies that roots notification sent
// before initialization is handled gracefully (goes to the pre-init
// notification path which currently marks rootsStale).
func TestRootsNotificationBeforeInit(t *testing.T) {
	srv := testutil.NewTestServer()

	// Send notification before init — should not panic
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})
	assert.Nil(t, resp)
}

// TestRootType verifies that Root struct serializes correctly to JSON
// matching the MCP spec format.
func TestRootType(t *testing.T) {
	root := core.Root{
		URI:  "file:///home/user/project",
		Name: "My Project",
	}

	data, err := json.Marshal(root)
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"uri":"file:///home/user/project"`)
	assert.Contains(t, string(data), `"name":"My Project"`)
}
