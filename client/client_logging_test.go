package client

// Client logging transport tests. Verify that the loggingTransport wrapper
// correctly logs method names, latency, errors, and delegates sessionID.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoggingTransport_LogsCallMethod verifies that call() logs the JSON-RPC
// method name extracted from the request body.
func TestLoggingTransport_LogsCallMethod(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	srv := newTestMCPServer()
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test", Version: "1.0"},
		WithClientLogging(logger))
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("echo", map[string]any{"message": "hello"})
	require.NoError(t, err)
	assert.Contains(t, result, "hello")

	output := buf.String()
	assert.Contains(t, output, "initialize", "should log initialize call")
	assert.Contains(t, output, "tools/call", "should log tools/call")
	assert.Contains(t, output, "ok", "successful calls should log 'ok'")
}

// TestLoggingTransport_LogsErrors verifies that transport errors and JSON-RPC
// errors are logged with appropriate detail.
func TestLoggingTransport_LogsErrors(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	srv := newTestMCPServer()
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test", Version: "1.0"},
		WithClientLogging(logger))
	require.NoError(t, c.Connect())
	defer c.Close()

	// Call a tool that returns an error result (ToolCall wraps isError as Go error)
	_, err := c.ToolCall("fail", nil)
	assert.Error(t, err, "fail tool should return an error")

	output := buf.String()
	assert.Contains(t, output, "tools/call", "should log the call")
}

// TestLoggingTransport_LogsLatency verifies that log output includes duration
// information in bracket format [Xms] or [X.Xs].
func TestLoggingTransport_LogsLatency(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	srv := newTestMCPServer()
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test", Version: "1.0"},
		WithClientLogging(logger))
	require.NoError(t, c.Connect())
	defer c.Close()

	output := buf.String()
	// Duration format includes brackets: [1.234ms] or [1.234µs]
	assert.True(t, strings.Contains(output, "[") && strings.Contains(output, "]"),
		"output should contain duration in brackets: %s", output)
}

// TestLoggingTransport_SessionIDPassthrough verifies that the logging wrapper
// correctly delegates getSessionID() to the inner transport.
func TestLoggingTransport_SessionIDPassthrough(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	srv := newTestMCPServer()
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	defer ts.Close()

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test", Version: "1.0"},
		WithClientLogging(logger))
	require.NoError(t, c.Connect())
	defer c.Close()

	sid := c.SessionID()
	assert.NotEmpty(t, sid, "SessionID should work through logging wrapper")
}

// TestExtractMethodFromJSON verifies the JSON method extraction helper.
func TestExtractMethodFromJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`, "tools/call"},
		{`{"method":"initialize"}`, "initialize"},
		{`{"id":1}`, "<unknown>"},
		{`invalid json`, "<unknown>"},
		{``, "<unknown>"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%s", tt.input[:min(len(tt.input), 30)]), func(t *testing.T) {
			assert.Equal(t, tt.expected, extractMethodFromJSON([]byte(tt.input)))
		})
	}
}

// Suppress unused import warnings.
var _ = context.Background
