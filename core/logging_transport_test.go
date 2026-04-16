package core

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTransport is a minimal Transport for testing the LoggingTransport decorator.
type mockTransport struct {
	callResp  *Response
	callErr   error
	notifyErr error
	sessionID string
	connected bool
}

func (m *mockTransport) Connect(ctx context.Context) error {
	m.connected = true
	return nil
}

func (m *mockTransport) Call(ctx context.Context, req *Request) (*Response, error) {
	return m.callResp, m.callErr
}

func (m *mockTransport) Notify(ctx context.Context, req *Request) error {
	return m.notifyErr
}

func (m *mockTransport) Close() error { return nil }

func (m *mockTransport) SessionID() string { return m.sessionID }

// TestLoggingTransport_CallLogsMethodAndLatency verifies that Call() logs the
// method name, direction arrows (→/←), and latency for both request and response.
func TestLoggingTransport_CallLogsMethodAndLatency(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	inner := &mockTransport{
		callResp:  &Response{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: "ok"},
		sessionID: "test-session",
	}
	lt := &LoggingTransport{Inner: inner, Logger: logger}

	require.NoError(t, lt.Connect(context.Background()))
	output := buf.String()
	assert.Contains(t, output, "connected")
	assert.Contains(t, output, "test-session")

	buf.Reset()
	resp, err := lt.Call(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	output = buf.String()
	assert.Contains(t, output, "→ tools/call", "should log outgoing request with arrow")
	assert.Contains(t, output, "← tools/call ok (", "should log incoming response with arrow and latency")
}

// TestLoggingTransport_NotifyLogsMethod verifies that Notify() logs the method
// name with a "notify" prefix.
func TestLoggingTransport_NotifyLogsMethod(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	inner := &mockTransport{sessionID: "s1"}
	lt := &LoggingTransport{Inner: inner, Logger: logger}

	err := lt.Notify(context.Background(), &Request{
		JSONRPC: "2.0", Method: "notifications/progress",
	})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "→ notify notifications/progress")
}

// TestLoggingTransport_LogBodiesIncludesJSON verifies that when LogBodies is
// true, the full JSON-RPC request and response bodies are included in logs.
func TestLoggingTransport_LogBodiesIncludesJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	inner := &mockTransport{
		callResp:  &Response{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: "hello"},
		sessionID: "s1",
	}
	lt := &LoggingTransport{Inner: inner, Logger: logger, LogBodies: true}

	_, err := lt.Call(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"echo"}`),
	})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, `"name":"echo"`, "request body should appear in log")
	assert.Contains(t, output, `"result"`, "response body should appear in log")
}

// TestLoggingTransport_NoBodiesByDefault verifies that when LogBodies is false
// (default), JSON-RPC bodies are NOT included — only method names and latency.
func TestLoggingTransport_NoBodiesByDefault(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	inner := &mockTransport{
		callResp:  &Response{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: "hello"},
		sessionID: "s1",
	}
	lt := &LoggingTransport{Inner: inner, Logger: logger} // LogBodies defaults to false

	_, err := lt.Call(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"echo"}`),
	})
	require.NoError(t, err)

	output := buf.String()
	assert.NotContains(t, output, `"name":"echo"`, "request body should NOT appear by default")
	assert.Contains(t, output, "tools/call", "method name should still appear")
}

// TestLoggingTransport_RPCErrorLogsCode verifies that JSON-RPC error responses
// are logged with the error code and message.
func TestLoggingTransport_RPCErrorLogsCode(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	inner := &mockTransport{
		callResp: &Response{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Error: &Error{Code: -32601, Message: "Method not found"},
		},
		sessionID: "s1",
	}
	lt := &LoggingTransport{Inner: inner, Logger: logger}

	resp, err := lt.Call(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error)

	output := buf.String()
	assert.Contains(t, output, "rpc-error")
	assert.Contains(t, output, "-32601")
	assert.Contains(t, output, "Method not found")
}

// TestLoggingTransport_SessionIDDelegates verifies that SessionID() is a pure
// passthrough to the inner transport.
func TestLoggingTransport_SessionIDDelegates(t *testing.T) {
	inner := &mockTransport{sessionID: "abc-123"}
	lt := &LoggingTransport{Inner: inner}
	assert.Equal(t, "abc-123", lt.SessionID())
}
