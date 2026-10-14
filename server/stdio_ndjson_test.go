package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// ndjsonInit is the initialize line from #1517. Sessions that go on to make
// further calls use ndjsonInitSession instead, because 2025-06-18 is not in
// supportedProtocolVersions and negotiates up to a revision that wants
// per-request _meta (#1519).
const (
	ndjsonInit        = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	ndjsonInitSession = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
)

// ndjsonPeer drives a RunIO server the way Claude Code and the Python and TS
// SDKs do: one JSON message per line, no Content-Length header.
type ndjsonPeer struct {
	t   *testing.T
	w   io.WriteCloser
	r   *bufio.Reader
	ctx context.Context
}

func startNDJSON(t *testing.T, srv *Server, opts ...StdioOption) *ndjsonPeer {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); cw.Close(); cr.Close() })
	go srv.RunIO(ctx, sr, sw, opts...)
	return &ndjsonPeer{t: t, w: cw, r: bufio.NewReader(cr), ctx: ctx}
}

func (p *ndjsonPeer) send(line string) {
	p.t.Helper()
	errc := make(chan error, 1)
	go func() {
		_, err := io.WriteString(p.w, line+"\n")
		errc <- err
	}()
	select {
	case err := <-errc:
		if err != nil {
			p.t.Fatalf("write: %v", err)
		}
	case <-time.After(5 * time.Second):
		p.t.Fatal("server stopped reading within 5s")
	}
}

// recv returns the next line the server wrote, failing after 5s. A server
// still speaking Content-Length lands here as a "Content-Length: N" line.
func (p *ndjsonPeer) recv() string {
	p.t.Helper()
	ch := make(chan string, 1)
	go func() {
		line, _ := p.r.ReadString('\n')
		ch <- line
	}()
	select {
	case line := <-ch:
		return line
	case <-time.After(5 * time.Second):
		p.t.Fatal("no response from server within 5s")
		return ""
	}
}

func (p *ndjsonPeer) recvResponse() core.Response {
	p.t.Helper()
	line := p.recv()
	if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		p.t.Fatalf("response is not one newline-terminated line: %q", line)
	}
	var resp core.Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		p.t.Fatalf("response line is not JSON: %q: %v", line, err)
	}
	return resp
}

// The repro from #1517: a newline-delimited initialize gets a newline-delimited
// answer.
func TestStdioNewlineDelimitedInitialize(t *testing.T) {
	p := startNDJSON(t, newTestServer())
	p.send(ndjsonInit)

	resp := p.recvResponse()
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", resp.ID)
	}
	var result struct {
		ServerInfo core.ServerInfo `json:"serverInfo"`
	}
	if err := resp.ResultAs(&result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("server name = %q, want test-server", result.ServerInfo.Name)
	}
}

// A full session: initialize, initialized notification (no reply), then a
// tool call whose argument is bigger than bufio.Scanner's 64 KiB default.
func TestStdioNewlineDelimitedToolCallLargeLine(t *testing.T) {
	p := startNDJSON(t, newTestServer())
	p.send(ndjsonInitSession)
	p.recvResponse()
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	big := strings.Repeat("x", 100_000)
	p.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"` + big + `"}}}`)

	resp := p.recvResponse()
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
	if string(resp.ID) != "2" {
		t.Fatalf("id = %s, want 2 (the initialized notification must not be answered)", resp.ID)
	}
	var result core.ToolResult
	if err := resp.ResultAs(&result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "echo: "+big {
		t.Errorf("echo result was truncated or wrong (len %d)", len(result.Content[0].Text))
	}
}

// Blank lines and CRLF endings between messages are tolerated.
func TestStdioNewlineDelimitedBlankLinesAndCRLF(t *testing.T) {
	p := startNDJSON(t, newTestServer())
	p.send("")
	p.send(ndjsonInit + "\r")

	resp := p.recvResponse()
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
}

// A malformed line gets a parse error on its own line and the session goes on.
func TestStdioNewlineDelimitedParseError(t *testing.T) {
	p := startNDJSON(t, newTestServer())
	p.send(ndjsonInitSession)
	p.recvResponse()

	p.send(`{"jsonrpc":"2.0","id":3,"method":`)
	resp := p.recvResponse()
	if resp.Error == nil || resp.Error.Code != core.ErrCodeParse {
		t.Fatalf("want parse error, got %+v", resp)
	}

	p.send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if resp := p.recvResponse(); resp.Error != nil || string(resp.ID) != "4" {
		t.Fatalf("ping after parse error: %+v", resp)
	}
}

// Server-initiated notifications follow the framing the client chose.
func TestStdioNewlineDelimitedNotification(t *testing.T) {
	srv := newTestServer()
	srv.RegisterTool(
		core.ToolDef{Name: "log-test", Description: "emits a log", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			core.EmitLog(ctx, core.LogInfo, "test-logger", "hello from stdio")
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}, nil
		},
	)
	p := startNDJSON(t, srv)
	p.send(ndjsonInitSession)
	p.recvResponse()
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(`{"jsonrpc":"2.0","id":2,"method":"logging/setLevel","params":{"level":"debug"}}`)
	p.recvResponse()
	p.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"log-test","arguments":{}}}`)

	var sawLog, sawResult bool
	for !(sawLog && sawResult) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		line := p.recv()
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("not a newline-delimited JSON message: %q", line)
		}
		switch {
		case msg.Method == "notifications/message":
			sawLog = true
		case string(msg.ID) == "3":
			sawResult = true
		}
	}
}
