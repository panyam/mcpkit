package server_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test input/output types ---

type greetInput struct {
	Name    string `json:"name"`
	Excited bool   `json:"excited,omitempty"`
}

type searchInput struct {
	Query      string `json:"query"      jsonschema:"description=Search query"`
	Genre      string `json:"genre,omitempty" jsonschema:"enum=fiction,enum=nonfiction,enum=science"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=Max results to return"`
}

type searchOutput struct {
	Results []string `json:"results"`
	Total   int      `json:"total"`
}

type nestedInput struct {
	User    userInfo `json:"user"`
	Message string   `json:"message"`
}

type userInfo struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// --- Schema Generation Tests ---

// TestTypedTool_SchemaGeneration verifies that InputSchema is correctly derived
// from the Go struct type, including property types, required fields (from
// omitempty), and descriptions (from jsonschema tags).
func TestTypedTool_SchemaGeneration(t *testing.T) {
	tool := core.TextTool[searchInput]("search", "Search things",
		func(ctx core.ToolContext, input searchInput) (string, error) {
			return "ok", nil
		},
	)

	schema := marshalSchema(t, tool.InputSchema)

	assert.Equal(t, "object", schema["type"])
	props := schema["properties"].(map[string]any)

	// query should be present with description
	query := props["query"].(map[string]any)
	assert.Equal(t, "string", query["type"])
	assert.Equal(t, "Search query", query["description"])

	// genre should have enum values
	genre := props["genre"].(map[string]any)
	assert.Equal(t, "string", genre["type"])
	enumVals := genre["enum"].([]any)
	assert.ElementsMatch(t, []any{"fiction", "nonfiction", "science"}, enumVals)

	// max_results should be integer
	maxResults := props["max_results"].(map[string]any)
	assert.Equal(t, "integer", maxResults["type"])

	// required: only "query" (others have omitempty)
	required := toStringSlice(schema["required"])
	assert.Equal(t, []string{"query"}, required)

	// additionalProperties should NOT be set (our default, opposite of go-sdk)
	_, hasAdditional := schema["additionalProperties"]
	assert.False(t, hasAdditional, "additionalProperties should not be set by default")
}

// TestTypedTool_NestedSchema verifies that nested struct types produce
// correctly inlined JSON Schema objects (no $ref/$defs).
func TestTypedTool_NestedSchema(t *testing.T) {
	tool := core.TextTool[nestedInput]("nested", "Nested input",
		func(ctx core.ToolContext, input nestedInput) (string, error) {
			return "ok", nil
		},
	)

	schema := marshalSchema(t, tool.InputSchema)
	props := schema["properties"].(map[string]any)

	// user should be an inlined object (not a $ref)
	user := props["user"].(map[string]any)
	assert.Equal(t, "object", user["type"])
	userProps := user["properties"].(map[string]any)
	assert.Contains(t, userProps, "name")
	assert.Contains(t, userProps, "email")

	// No $defs or $ref at top level
	assert.NotContains(t, schema, "$defs")
	assert.NotContains(t, schema, "$ref")
}

// TestTypedTool_OutputSchemaGeneration verifies OutputSchema is generated for
// struct Out types, and absent for string and core.ToolResult Out types.
func TestTypedTool_OutputSchemaGeneration(t *testing.T) {
	// Struct Out → OutputSchema present
	structTool := core.TypedTool[greetInput, searchOutput]("a", "desc",
		func(ctx core.ToolContext, input greetInput) (searchOutput, error) {
			return searchOutput{}, nil
		},
	)
	assert.NotNil(t, structTool.OutputSchema, "struct Out should generate OutputSchema")
	outSchema := marshalSchema(t, structTool.OutputSchema)
	assert.Equal(t, "object", outSchema["type"])
	outProps := outSchema["properties"].(map[string]any)
	assert.Contains(t, outProps, "results")
	assert.Contains(t, outProps, "total")

	// String Out → no OutputSchema
	stringTool := core.TextTool[greetInput]("b", "desc",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return "", nil
		},
	)
	assert.Nil(t, stringTool.OutputSchema, "string Out should not generate OutputSchema")

	// core.ToolResult Out → no OutputSchema
	resultTool := core.TypedTool[greetInput, core.ToolResult]("c", "desc",
		func(ctx core.ToolContext, input greetInput) (core.ToolResult, error) {
			return core.TextResult("raw"), nil
		},
	)
	assert.Nil(t, resultTool.OutputSchema, "ToolResult Out should not generate OutputSchema")
}

// TestTextTool_Sugar verifies that TextTool[In] produces identical schema to
// TypedTool[In, string].
func TestTextTool_Sugar(t *testing.T) {
	text := core.TextTool[searchInput]("a", "desc",
		func(ctx core.ToolContext, input searchInput) (string, error) {
			return "", nil
		},
	)
	typed := core.TypedTool[searchInput, string]("a", "desc",
		func(ctx core.ToolContext, input searchInput) (string, error) {
			return "", nil
		},
	)

	textSchema := marshalSchema(t, text.InputSchema)
	typedSchema := marshalSchema(t, typed.InputSchema)
	assert.Equal(t, textSchema, typedSchema)
	assert.Nil(t, text.OutputSchema)
	assert.Nil(t, typed.OutputSchema)
}

// --- Handler Behavior Tests ---

// TestTypedTool_TypeSafeDeserialization verifies that the handler receives
// correctly deserialized typed input when called through the server dispatch.
func TestTypedTool_TypeSafeDeserialization(t *testing.T) {
	var captured greetInput

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[greetInput]("greet", "Greet someone",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			captured = input
			return "Hello, " + input.Name, nil
		},
	))
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		testutil.ToolCallRequest("greet", map[string]any{
			"name":    "Alice",
			"excited": true,
		}))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	// Verify typed input was correctly deserialized
	assert.Equal(t, "Alice", captured.Name)
	assert.True(t, captured.Excited)

	// Verify text result
	var result core.ToolResult
	require.NoError(t, resp.ResultAs(&result))
	assert.Equal(t, "Hello, Alice", result.Content[0].Text)
}

// TestTypedTool_InvalidInput verifies that malformed input produces a tool error
// (isError=true), not a protocol error.
func TestTypedTool_InvalidInput(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[greetInput]("greet", "Greet",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return "should not reach here", nil
		},
	))
	testutil.InitHandshake(srv)

	// Send excited as string instead of bool → deserialization error
	resp, _ := srv.Dispatch(context.Background(),
		testutil.ToolCallRequest("greet", map[string]any{
			"name":    "Alice",
			"excited": "not-a-bool",
		}))
	require.NotNil(t, resp)
	// Schema validation may catch this first (as a protocol error with code -32602),
	// or deserialization catches it (as a tool error with isError=true).
	// Either way, the tool handler should NOT be reached.
	// Accept either error path — both are correct behavior.
	if resp.Error != nil {
		assert.Equal(t, -32602, resp.Error.Code, "schema validation should reject bad input")
	} else {
		var result core.ToolResult
		require.NoError(t, resp.ResultAs(&result))
		assert.True(t, result.IsError, "deserialization failure should produce isError=true")
		assert.Contains(t, result.Content[0].Text, "invalid arguments")
	}
}

// TestTypedTool_StructuredOutput verifies that TypedTool with a struct Out type
// populates StructuredContent and returns a text fallback.
func TestTypedTool_StructuredOutput(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TypedTool[greetInput, searchOutput]("search", "Search",
		func(ctx core.ToolContext, input greetInput) (searchOutput, error) {
			return searchOutput{Results: []string{"book1", "book2"}, Total: 2}, nil
		},
	))
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		testutil.ToolCallRequest("search", map[string]any{"name": "test"}))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	var result core.ToolResult
	require.NoError(t, resp.ResultAs(&result))
	assert.False(t, result.IsError)
	// Text fallback should contain JSON
	assert.Contains(t, result.Content[0].Text, "book1")
	// StructuredContent should be populated
	assert.NotNil(t, result.StructuredContent)
	structured, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok, "StructuredContent should unmarshal as map")
	assert.Equal(t, float64(2), structured["total"])
}

// TestTypedTool_ToolResultPassthrough verifies that TypedTool with core.ToolResult
// Out type passes the handler's result through without modification.
func TestTypedTool_ToolResultPassthrough(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TypedTool[greetInput, core.ToolResult]("multi", "Multi-content",
		func(ctx core.ToolContext, input greetInput) (core.ToolResult, error) {
			return core.ToolResult{
				Content: []core.Content{
					{Type: "text", Text: "line 1"},
					{Type: "text", Text: "line 2"},
				},
			}, nil
		},
	))
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		testutil.ToolCallRequest("multi", map[string]any{"name": "test"}))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	var result core.ToolResult
	require.NoError(t, resp.ResultAs(&result))
	require.Len(t, result.Content, 2)
	assert.Equal(t, "line 1", result.Content[0].Text)
	assert.Equal(t, "line 2", result.Content[1].Text)
}

// TestTypedTool_TypedContext verifies that the handler receives a ToolContext
// with working EmitLog and EmitProgress methods (not a bare context.Context).
func TestTypedTool_TypedContext(t *testing.T) {
	var gotContext bool

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[greetInput]("ctx-test", "Context test",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			// These should not panic — ToolContext provides them.
			ctx.EmitLog(core.LogInfo, "test", "hello")
			gotContext = true
			return "ok", nil
		},
	))
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		testutil.ToolCallRequest("ctx-test", map[string]any{"name": "test"}))
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	assert.True(t, gotContext, "handler should have been called with working ToolContext")
}

// --- Options Tests ---

// TestTypedTool_Options verifies that TypedToolOption values (timeout, annotations,
// meta) are correctly applied to the generated ToolDef.
func TestTypedTool_Options(t *testing.T) {
	tool := core.TextTool[greetInput]("opt-test", "Options test",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return "ok", nil
		},
		core.WithToolAnnotations(map[string]any{"experimental": true}),
		core.WithTypedToolTimeout(5*time.Second),
		core.WithToolMeta(&core.ToolMeta{}),
	)

	assert.Equal(t, "opt-test", tool.Name)
	assert.Equal(t, "Options test", tool.Description)
	assert.Equal(t, 5*time.Second, tool.Timeout)
	assert.Equal(t, map[string]any{"experimental": true}, tool.Annotations)
	assert.NotNil(t, tool.Meta)
}

// --- Integration Test ---

// TestTypedTool_ForAllTransports exercises a TypedTool through all 4 MCP transports
// (Streamable HTTP, SSE, in-process, stdio) to verify end-to-end behavior.
func TestTypedTool_ForAllTransports(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "typed-test", Version: "1.0"})
	srv.Register(core.TextTool[greetInput]("greet", "Greet someone",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			greeting := "Hello, " + input.Name
			if input.Excited {
				greeting += "!"
			}
			return greeting, nil
		},
	))

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		result, err := c.ToolCall("greet", map[string]any{
			"name":    "World",
			"excited": true,
		})
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", result)
	})
}

// --- Helpers ---

// marshalSchema converts an InputSchema/OutputSchema (json.RawMessage or any)
// to a map for assertion.
func marshalSchema(t *testing.T, schema any) map[string]any {
	t.Helper()
	var data []byte
	var err error
	switch s := schema.(type) {
	case json.RawMessage:
		data = s
	default:
		data, err = json.Marshal(s)
		require.NoError(t, err)
	}
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// toStringSlice extracts a []string from an any ([]any with string elements).
func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i] = x.(string)
	}
	return out
}
