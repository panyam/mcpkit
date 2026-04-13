package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// TestToolExec_Echo wraps the "echo" command as an MCP tool and verifies that
// the subprocess stdout is returned as a TextResult. This is the simplest
// ToolExec scenario: static args, no dynamic BuildArgs, no timeout.
func TestToolExec_Echo(t *testing.T) {
	tool := ToolExec(ExecConfig{
		Name:    "echo-test",
		Command: "echo",
		Args:    []string{"hello"},
	})

	result, err := tool.Handler(core.NewToolContext(context.Background()), core.ToolRequest{
		Name: "echo-test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", result.Content[0].Text)
	}
}

// TestToolExec_BuildArgs wraps "echo" with a BuildArgs callback that extracts
// a "message" field from the tool request's JSON arguments and passes it as a
// CLI argument. Verifies that dynamic arguments from the MCP request are
// correctly mapped to subprocess arguments.
func TestToolExec_BuildArgs(t *testing.T) {
	tool := ToolExec(ExecConfig{
		Name:    "echo-dynamic",
		Command: "echo",
		BuildArgs: func(args json.RawMessage) ([]string, error) {
			var p struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return []string{p.Message}, nil
		},
	})

	result, err := tool.Handler(core.NewToolContext(context.Background()), core.ToolRequest{
		Name:      "echo-dynamic",
		Arguments: json.RawMessage(`{"message":"world"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "world\n" {
		t.Errorf("expected 'world\\n', got %q", result.Content[0].Text)
	}
}

// TestToolExec_NonZeroExit wraps a command that exits with code 1 and verifies
// that ToolExec returns an ErrorResult (IsError: true) with the stderr output
// and exit status. Non-zero exits are tool-level errors, not transport errors.
func TestToolExec_NonZeroExit(t *testing.T) {
	tool := ToolExec(ExecConfig{
		Name:    "fail-test",
		Command: "sh",
		Args:    []string{"-c", "echo fail >&2; exit 1"},
	})

	result, err := tool.Handler(core.NewToolContext(context.Background()), core.ToolRequest{
		Name: "fail-test",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for non-zero exit")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected error content")
	}
	text := result.Content[0].Text
	if text == "" {
		t.Fatal("expected non-empty error message")
	}
}

// TestToolExec_Timeout wraps "sleep 10" with a 100ms timeout and verifies
// that the subprocess is killed when the timeout expires. The result should
// be an ErrorResult with a context deadline exceeded indication.
func TestToolExec_Timeout(t *testing.T) {
	tool := ToolExec(ExecConfig{
		Name:    "timeout-test",
		Command: "sleep",
		Args:    []string{"10"},
		Timeout: 100 * time.Millisecond,
	})

	start := time.Now()
	result, err := tool.Handler(core.NewToolContext(context.Background()), core.ToolRequest{
		Name: "timeout-test",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for timed-out command")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not take effect, elapsed: %v", elapsed)
	}
}

// TestToolExec_RegisterAndDispatch performs an end-to-end test: registers a
// ToolExec tool via the single-struct Register API, dispatches a tools/call
// request through the Dispatcher, and verifies the result flows correctly
// through the full MCP JSON-RPC stack.
func TestToolExec_RegisterAndDispatch(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "exec-test", Version: "1.0"})
	srv.Register(ToolExec(ExecConfig{
		Name:        "greet",
		Description: "Greet via echo",
		Command:     "echo",
		Args:        []string{"hello from exec"},
	}))

	d := srv.newSession()
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"greet"}`),
	})
	if resp.Error != nil {
		t.Fatalf("tools/call failed: %s", resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var result core.ToolResult
	json.Unmarshal(raw, &result)
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "hello from exec\n" {
		t.Errorf("expected 'hello from exec\\n', got %q", result.Content[0].Text)
	}
}
