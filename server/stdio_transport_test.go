package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// TestStdioFrameRoundTrip verifies that writeFrame and readFrame produce and
// consume Content-Length framed messages correctly. A message written with
// writeFrame should be readable by readFrame with identical content.
func TestStdioFrameRoundTrip(t *testing.T) {
	msg := `{"jsonrpc":"2.0","id":1,"method":"test"}`

	var buf bytes.Buffer
	if err := gohttp.WriteFrame(&buf, []byte(msg)); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	// Verify wire format: header + separator + body.
	raw := buf.String()
	expected := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(msg), msg)
	if raw != expected {
		t.Errorf("unexpected wire format:\n got: %q\nwant: %q", raw, expected)
	}

	// Read it back.
	reader := bufio.NewReader(&buf)
	body, err := gohttp.ReadFrame(reader)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(body) != msg {
		t.Errorf("readFrame body = %q, want %q", body, msg)
	}
}

// TestStdioFrameMultipleHeaders verifies that readFrame correctly handles
// messages with multiple headers, ignoring unknown headers and extracting
// only Content-Length.
func TestStdioFrameMultipleHeaders(t *testing.T) {
	msg := `{"test":true}`
	frame := "Content-Type: application/json\r\nContent-Length: 13\r\nX-Custom: foo\r\n\r\n" + msg

	reader := bufio.NewReader(strings.NewReader(frame))
	body, err := gohttp.ReadFrame(reader)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(body) != msg {
		t.Errorf("body = %q, want %q", body, msg)
	}
}

// TestStdioFrameMalformedHeader verifies that readFrame returns an error
// for headers that cannot be parsed (missing colon separator).
func TestStdioFrameMalformedHeader(t *testing.T) {
	frame := "BADHEADER\r\n\r\n"
	reader := bufio.NewReader(strings.NewReader(frame))
	_, err := gohttp.ReadFrame(reader)
	if err == nil {
		t.Fatal("expected error for malformed header")
	}
	if !strings.Contains(err.Error(), "malformed header") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStdioFrameInvalidContentLength verifies that readFrame returns an error
// when Content-Length is not a valid integer.
func TestStdioFrameInvalidContentLength(t *testing.T) {
	frame := "Content-Length: abc\r\n\r\n"
	reader := bufio.NewReader(strings.NewReader(frame))
	_, err := gohttp.ReadFrame(reader)
	if err == nil {
		t.Fatal("expected error for invalid Content-Length")
	}
	if !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStdioFrameMissingContentLength verifies that readFrame returns an error
// when no Content-Length header is present in the message headers.
func TestStdioFrameMissingContentLength(t *testing.T) {
	frame := "Content-Type: application/json\r\n\r\n"
	reader := bufio.NewReader(strings.NewReader(frame))
	_, err := gohttp.ReadFrame(reader)
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
	if !strings.Contains(err.Error(), "missing Content-Length") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStdioFrameNegativeContentLength verifies that readFrame rejects
// negative Content-Length values.
func TestStdioFrameNegativeContentLength(t *testing.T) {
	frame := "Content-Length: -5\r\n\r\n"
	reader := bufio.NewReader(strings.NewReader(frame))
	_, err := gohttp.ReadFrame(reader)
	if err == nil {
		t.Fatal("expected error for negative Content-Length")
	}
	if !strings.Contains(err.Error(), "negative Content-Length") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStdioFramePartialRead verifies that readFrame returns an error when
// the body is shorter than what Content-Length declares (EOF mid-body).
func TestStdioFramePartialRead(t *testing.T) {
	frame := "Content-Length: 100\r\n\r\nshort"
	reader := bufio.NewReader(strings.NewReader(frame))
	_, err := gohttp.ReadFrame(reader)
	if err == nil {
		t.Fatal("expected error for partial body read")
	}
}

// TestStdioParseError verifies that the stdio transport returns a JSON-RPC
// parse error response when it receives malformed JSON, rather than silently
// skipping the message. This is a key spec requirement and the fix for the
// bug described in issue #3.
func TestStdioParseError(t *testing.T) {
	// Send a valid frame with invalid JSON content.
	badJSON := "not json at all"
	var input bytes.Buffer
	gohttp.WriteFrame(&input, []byte(badJSON))

	var output bytes.Buffer
	srv := newTestServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.RunStdio(ctx, WithStdioInput(&input), WithStdioOutput(&output))
	}()

	// Wait for the server to process (EOF from input will cause clean exit).
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunStdio: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("RunStdio did not exit")
	}

	// Read the error response from output.
	reader := bufio.NewReader(&output)
	body, err := gohttp.ReadFrame(reader)
	if err != nil {
		t.Fatalf("readFrame from output: %v", err)
	}

	var resp core.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != core.ErrCodeParse {
		t.Errorf("error code = %d, want %d (parse error)", resp.Error.Code, core.ErrCodeParse)
	}
}

// TestStdioEOFCleanShutdown verifies that the stdio transport exits cleanly
// (returns nil error) when the input stream reaches EOF, simulating a client
// disconnect.
func TestStdioEOFCleanShutdown(t *testing.T) {
	// Empty input → immediate EOF.
	var input bytes.Buffer
	var output bytes.Buffer
	srv := newTestServer()

	done := make(chan error, 1)
	go func() {
		done <- srv.RunStdio(context.Background(), WithStdioInput(&input), WithStdioOutput(&output))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunStdio should return nil on EOF, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunStdio did not exit on EOF")
	}
}

// TestStdioCancellation verifies that the stdio transport exits promptly
// when the context is cancelled, supporting clean shutdown via signal handling.
func TestStdioCancellation(t *testing.T) {
	// Use a pipe that never sends data — the server blocks on read.
	pr, pw := io.Pipe()
	defer pw.Close()

	var output bytes.Buffer
	srv := newTestServer()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.RunStdio(ctx, WithStdioInput(pr), WithStdioOutput(&output))
	}()

	// Cancel after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunStdio did not exit on cancellation")
	}
}

// TestStdioRequestResponse verifies the full request-response cycle over stdio:
// send a JSON-RPC initialize request, receive a valid response with server info.
func TestStdioRequestResponse(t *testing.T) {
	srv := newTestServer()
	sr, cw := io.Pipe() // server reads from sr, client writes to cw
	cr, sw := io.Pipe() // client reads from cr, server writes to sw

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.RunStdio(ctx, WithStdioInput(sr), WithStdioOutput(sw))

	// Send initialize request.
	initReq := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	gohttp.WriteFrame(cw, []byte(initReq))

	// Read response.
	reader := bufio.NewReader(cr)
	body, err := gohttp.ReadFrame(reader)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}

	var resp core.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	// Check server info in result.
	var result struct {
		ServerInfo core.ServerInfo `json:"serverInfo"`
	}
	resp.ResultAs(&result)
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("server name = %q, want test-server", result.ServerInfo.Name)
	}

	// Clean shutdown.
	cw.Close()
}

// TestStdioNotificationDelivery verifies that server-to-client notifications
// (e.g., logging messages emitted during tool execution) are written to stdout
// as Content-Length framed messages.
func TestStdioNotificationDelivery(t *testing.T) {
	srv := newTestServer()

	// Register a tool that emits a log notification.
	srv.RegisterTool(
		core.ToolDef{Name: "log-test", Description: "emits a log", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitLog(ctx, core.LogInfo, "test-logger", "hello from stdio")
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}, nil
		},
	)

	sr, cw := io.Pipe()
	cr, sw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.RunStdio(ctx, WithStdioInput(sr), WithStdioOutput(sw))

	reader := bufio.NewReader(cr)

	// Initialize.
	initReq := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	gohttp.WriteFrame(cw, []byte(initReq))
	gohttp.ReadFrame(reader) // consume init response

	// Send initialized notification.
	initedNotif := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	gohttp.WriteFrame(cw, []byte(initedNotif))

	// Set log level so notifications are emitted.
	setLevel := `{"jsonrpc":"2.0","id":"2","method":"logging/setLevel","params":{"level":"info"}}`
	gohttp.WriteFrame(cw, []byte(setLevel))
	gohttp.ReadFrame(reader) // consume setLevel response

	// Call the tool that emits a log.
	toolCall := `{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"log-test","arguments":{}}}`
	gohttp.WriteFrame(cw, []byte(toolCall))

	// We should receive a notification before the tool result.
	// Read frames until we get the tool response.
	var gotNotification bool
	for i := 0; i < 5; i++ {
		body, err := gohttp.ReadFrame(reader)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}

		var msg struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		json.Unmarshal(body, &msg)

		if msg.Method == "notifications/message" {
			gotNotification = true
			continue
		}
		if msg.ID != nil && string(msg.ID) == `"3"` {
			// Tool response received.
			break
		}
	}

	if !gotNotification {
		t.Error("expected notifications/message before tool result")
	}

	cw.Close()
}

// TestStdioMultipleFrames verifies that multiple Content-Length framed messages
// can be written and read sequentially from the same stream.
func TestStdioMultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	msgs := []string{
		`{"a":1}`,
		`{"b":2}`,
		`{"c":3}`,
	}
	for _, msg := range msgs {
		if err := gohttp.WriteFrame(&buf, []byte(msg)); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}

	reader := bufio.NewReader(&buf)
	for _, want := range msgs {
		body, err := gohttp.ReadFrame(reader)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if string(body) != want {
			t.Errorf("got %q, want %q", body, want)
		}
	}
}

// newTestServer creates a minimal MCP server with an echo tool for testing.
func newTestServer() *Server {
	srv := NewServer(core.ServerInfo{Name: "test-server", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "echoes input",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct{ Message string }
			json.Unmarshal(req.Arguments, &args)
			return core.ToolResult{
				Content: []core.Content{{Type: "text", Text: "echo: " + args.Message}},
			}, nil
		},
	)
	return srv
}
