package server_test

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterToolSingleStruct verifies that server.Register(server.Tool{...})
// correctly registers a tool that can be called via dispatch. This is the
// single-struct alternative to the two-argument RegisterTool(def, handler).
func TestRegisterToolSingleStruct(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "greet",
			Description: "Greets the user",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("hello"), nil
		},
	})

	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(), testutil.ToolCallRequest("greet", nil))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}

// TestRegisterResourceSingleStruct verifies that server.Register(server.Resource{...})
// correctly registers a resource accessible via resources/read.
func TestRegisterResourceSingleStruct(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Resource{
		ResourceDef: core.ResourceDef{
			URI:      "test://data",
			Name:     "Test Data",
			MimeType: "text/plain",
		},
		Handler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "text/plain", Text: "data content",
				}},
			}, nil
		},
	})

	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(), testutil.ResourceReadRequest("test://data"))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}

// TestRegisterPromptSingleStruct verifies that server.Register(server.Prompt{...})
// correctly registers a prompt accessible via prompts/get.
func TestRegisterPromptSingleStruct(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Prompt{
		PromptDef: core.PromptDef{
			Name:        "greeting",
			Description: "A greeting prompt",
		},
		Handler: func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "assistant",
					Content: core.Content{Type: "text", Text: "Hello!"},
				}},
			}, nil
		},
	})

	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(), testutil.PromptGetRequest("greeting", nil))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}

// TestRegisterMixed verifies that Register accepts a mix of tools, resources,
// and prompts in a single call.
func TestRegisterMixed(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(
		server.Tool{
			ToolDef: core.ToolDef{Name: "t1", Description: "tool", InputSchema: map[string]any{"type": "object"}},
			Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
				return core.TextResult("t1"), nil
			},
		},
		server.Resource{
			ResourceDef: core.ResourceDef{URI: "test://r1", Name: "resource"},
			Handler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
				return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: req.URI, Text: "r1"}}}, nil
			},
		},
		server.Prompt{
			PromptDef: core.PromptDef{Name: "p1", Description: "prompt"},
			Handler: func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
				return core.PromptResult{}, nil
			},
		},
	)

	testutil.InitHandshake(srv)

	// All three should be dispatched successfully
	resp, _ := srv.Dispatch(context.Background(), testutil.ToolCallRequest("t1", nil))
	require.Nil(t, resp.Error, "tool should work")

	resp, _ = srv.Dispatch(context.Background(), testutil.ResourceReadRequest("test://r1"))
	require.Nil(t, resp.Error, "resource should work")

	resp, _ = srv.Dispatch(context.Background(), testutil.PromptGetRequest("p1", nil))
	require.Nil(t, resp.Error, "prompt should work")
}

// TestExistingTwoArgAPIStillWorks verifies that the original RegisterTool(def, handler)
// API continues to work alongside the new Register() method.
func TestExistingTwoArgAPIStillWorks(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	// Old API
	srv.RegisterTool(
		core.ToolDef{Name: "old-style", Description: "two-arg", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("old"), nil
		},
	)
	// New API
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{Name: "new-style", Description: "single-struct", InputSchema: map[string]any{"type": "object"}},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("new"), nil
		},
	})

	testutil.InitHandshake(srv)

	resp1, _ := srv.Dispatch(context.Background(), testutil.ToolCallRequest("old-style", nil))
	resp2, _ := srv.Dispatch(context.Background(), testutil.ToolCallRequest("new-style", nil))
	assert.Nil(t, resp1.Error, "old-style should work")
	assert.Nil(t, resp2.Error, "new-style should work")
}
