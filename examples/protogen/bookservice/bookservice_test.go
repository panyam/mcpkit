package main

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	booksv1 "github.com/panyam/mcpkit/examples/protogen/bookservice/gen/bookservice/v1"
)

// newTestServer creates a BookService MCP server for testing.
func newTestServer() *server.Server {
	impl := &bookService{}
	srv := server.NewServer(core.ServerInfo{Name: "bookservice-test", Version: "0.1.0"})
	booksv1.RegisterBookServiceMCP(srv, impl)
	booksv1.RegisterBookServiceMCPResources(srv, impl)
	booksv1.RegisterBookServiceMCPPrompts(srv, impl)
	booksv1.RegisterBookServiceMCPCompletions(srv, impl)
	return srv
}

// TestToolCall verifies the books_search tool works end-to-end through
// the generated MCP registration code.
func TestToolCall(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCallFull("books_search", map[string]any{
		"query":       "programming",
		"max_results": 2,
		"genre":       "programming",
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	// Result summary template should have rendered.
	assert.Contains(t, result.Content[0].Text, "Found")
	assert.Contains(t, result.Content[0].Text, "books matching query")
	// Structured content should be present (structured_output: true).
	assert.NotNil(t, result.StructuredContent)
}

// TestResourceRead verifies the book://{book_id} template resource works
// end-to-end through the generated registration code.
func TestResourceRead(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	text, err := c.ReadResource("book://1")
	require.NoError(t, err)
	assert.Contains(t, text, "The Go Programming Language")
}

// TestPromptGet verifies the books_summarize prompt works end-to-end.
func TestPromptGet(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// List prompts.
	result, err := c.Call("prompts/list", nil)
	require.NoError(t, err)

	var listResult core.PromptsListResult
	require.NoError(t, result.Unmarshal(&listResult))
	require.GreaterOrEqual(t, len(listResult.Prompts), 2)

	// Find our prompt.
	var found bool
	for _, p := range listResult.Prompts {
		if p.Name == "books_summarize" {
			found = true
			assert.Equal(t, "Generate a summary of a book", p.Description)
			require.Len(t, p.Arguments, 2)
			assert.Equal(t, "book_id", p.Arguments[0].Name)
			assert.True(t, p.Arguments[0].Required)
			assert.Equal(t, "style", p.Arguments[1].Name)
			assert.False(t, p.Arguments[1].Required)
		}
	}
	assert.True(t, found, "books_summarize prompt not found in list")
}

// TestListTools verifies all registered tools appear in tools/list.
func TestListTools(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	tools, err := c.ListTools()
	require.NoError(t, err)

	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	assert.Contains(t, names, "books_search")
}

// TestListResources verifies all registered resources appear in resources/list
// and resources/templates/list.
func TestListResources(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// Template resources show up in resources/templates/list.
	result, err := c.Call("resources/templates/list", nil)
	require.NoError(t, err)

	var templResult core.ResourceTemplatesListResult
	require.NoError(t, result.Unmarshal(&templResult))

	var templateURIs []string
	for _, t := range templResult.ResourceTemplates {
		templateURIs = append(templateURIs, t.URITemplate)
	}
	assert.Contains(t, templateURIs, "book://{book_id}")
	assert.Contains(t, templateURIs, "author://{author_id}/books")
}

// TestCompletion verifies that completion/complete works end-to-end for
// the book_id field on the resource template.
func TestCompletion(t *testing.T) {
	srv := newTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// Complete book_id on the resource template.
	result, err := c.Call("completion/complete", map[string]any{
		"ref": map[string]any{
			"type": "ref/resource",
			"uri":  "book://{book_id}",
		},
		"argument": map[string]any{
			"name":  "book_id",
			"value": "1",
		},
	})
	require.NoError(t, err)

	var completeResult core.CompletionCompleteResult
	require.NoError(t, result.Unmarshal(&completeResult))
	assert.Contains(t, completeResult.Completion.Values, "1")
}
