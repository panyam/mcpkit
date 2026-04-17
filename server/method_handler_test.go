package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCustomMethod_Dispatch verifies that a custom JSON-RPC method is dispatched
// to the registered handler and returns the expected response.
func TestCustomMethod_Dispatch(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithMethodHandler("custom/echo", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
			var p struct{ Msg string `json:"msg"` }
			json.Unmarshal(params, &p)
			return core.NewResponse(id, map[string]string{"echo": p.Msg})
		}),
	)

	testutil.InitHandshake(srv)
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "custom/echo",
		Params: json.RawMessage(`{"msg":"hello"}`),
	})
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	var result map[string]string
	resp.ResultAs(&result)
	assert.Equal(t, "hello", result["echo"])
}

// TestCustomMethod_UnknownStillReturnsMethodNotFound verifies that unregistered
// methods still return -32601 MethodNotFound when custom handlers exist.
func TestCustomMethod_UnknownStillReturnsMethodNotFound(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithMethodHandler("custom/known", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
			return core.NewResponse(id, "ok")
		}),
	)

	testutil.InitHandshake(srv)
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "custom/unknown",
	})
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, core.ErrCodeMethodNotFound, resp.Error.Code)
}

// TestCustomMethod_BuiltinOverridePanics verifies that registering a handler
// for a built-in MCP method panics at startup (safety guard).
func TestCustomMethod_BuiltinOverridePanics(t *testing.T) {
	assert.Panics(t, func() {
		server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
			server.WithMethodHandler("tools/list", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
				return core.NewResponse(id, "hijacked")
			}),
		)
	})
}

// TestCustomMethod_RequiresInitialized verifies that custom methods require
// the session to be initialized (same gate as tools/resources).
func TestCustomMethod_RequiresInitialized(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithMethodHandler("custom/test", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
			return core.NewResponse(id, "ok")
		}),
	)

	// Don't initialize — just dispatch directly.
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "custom/test",
	})
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "not initialized")
}

// TestCustomMethod_HandleMethod verifies that Server.HandleMethod (dynamic
// registration) works the same as WithMethodHandler (option).
func TestCustomMethod_HandleMethod(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.HandleMethod("custom/dynamic", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		return core.NewResponse(id, "dynamic")
	})

	testutil.InitHandshake(srv)
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "custom/dynamic",
	})
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}

// TestCustomMethod_ForAllTransports verifies that custom methods work across
// all 4 MCP transport types (Streamable HTTP, SSE, in-process, stdio).
func TestCustomMethod_ForAllTransports(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithMethodHandler("custom/ping", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
			return core.NewResponse(id, map[string]string{"pong": "custom"})
		}),
	)

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		result, err := c.Call("custom/ping", nil)
		require.NoError(t, err)
		var resp map[string]string
		result.Unmarshal(&resp)
		assert.Equal(t, "custom", resp["pong"])
	})
}
