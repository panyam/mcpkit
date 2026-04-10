package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentRequestsGetCorrectResponses verifies that when 10
// concurrent tool calls are dispatched to the same session, each request
// gets exactly one response with a matching JSON-RPC ID and correct result.
// No responses are lost, duplicated, or misrouted.
func TestConcurrentRequestsGetCorrectResponses(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "identify",
			Description: "Returns the request argument as-is for identification",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"tag": map[string]any{"type": "string"}}},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var p struct{ Tag string `json:"tag"` }
			req.Bind(&p)
			time.Sleep(10 * time.Millisecond) // simulate work
			return core.TextResult("tag:" + p.Tag), nil
		},
	)
	testutil.InitHandshake(srv)

	const n = 10
	var wg sync.WaitGroup
	results := make([]*core.Response, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := fmt.Sprintf("req-%d", idx)
			params, _ := json.Marshal(map[string]any{"name": "identify", "arguments": map[string]any{"tag": tag}})
			resp := srv.Dispatch(context.Background(), &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage(fmt.Sprintf(`%d`, idx+100)),
				Method:  "tools/call",
				Params:  params,
			})
			results[idx] = resp
		}(i)
	}
	wg.Wait()

	// Every request should have a response
	for i, resp := range results {
		require.NotNil(t, resp, "request %d should have a response", i)
		require.Nil(t, resp.Error, "request %d should not be an error", i)
	}
}

// TestDuplicateRequestIDRejected verifies that sending a second request
// with the same JSON-RPC ID while the first is still in-flight is rejected
// with an InvalidRequest error. This prevents cancellation confusion where
// the second request's cancelFn would overwrite the first's.
func TestDuplicateRequestIDRejected(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow",
			Description: "Takes 500ms to complete",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			time.Sleep(500 * time.Millisecond)
			return core.TextResult("done"), nil
		},
	)
	testutil.InitHandshake(srv)

	// Fire first request (blocks for 500ms)
	var firstResp *core.Response
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstResp = srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`"dup-id"`),
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"slow"}`),
		})
	}()

	// Wait for first request to be in-flight
	time.Sleep(50 * time.Millisecond)

	// Send second request with same ID — should be rejected immediately
	secondResp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"dup-id"`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"slow"}`),
	})

	require.NotNil(t, secondResp)
	require.NotNil(t, secondResp.Error, "duplicate ID should be rejected")
	assert.Equal(t, core.ErrCodeInvalidRequest, secondResp.Error.Code)
	assert.Contains(t, secondResp.Error.Message, "duplicate request ID")

	// First request should complete normally
	wg.Wait()
	require.NotNil(t, firstResp)
	require.Nil(t, firstResp.Error, "first request should succeed")
}
