package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pipePair wires a conn to a fake server, returning the conn plus the reader
// and writer the server side uses.
func pipePair(t *testing.T) (*conn, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	clientReads, serverWrites := io.Pipe()
	serverReads, clientWrites := io.Pipe()
	c := newConn(clientWrites, clientReads)
	t.Cleanup(func() { _ = clientWrites.Close(); _ = serverWrites.Close() })
	return c, bufio.NewReader(serverReads), serverWrites
}

func serverSend(t *testing.T, w io.Writer, msg *message) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := io.WriteString(w, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
}

func TestReadFrameSkipsUnknownHeaders(t *testing.T) {
	raw := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"Content-Length: 2\r\n" +
		"X-Whatever: 1\r\n" +
		"\r\n" +
		"{}"
	got, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("body = %q, want %q", got, "{}")
	}
}

func TestReadFrameRejectsMissingContentLength(t *testing.T) {
	_, err := readFrame(bufio.NewReader(strings.NewReader("X-Only: 1\r\n\r\n{}")))
	if err == nil {
		t.Fatal("want an error for a frame with no Content-Length")
	}
}

func TestCallRoundTrip(t *testing.T) {
	c, serverIn, serverOut := pipePair(t)
	go c.run()

	go func() {
		frame, err := readFrame(serverIn)
		if err != nil {
			return
		}
		var got message
		_ = json.Unmarshal(frame, &got)
		serverSend(t, serverOut, &message{JSONRPC: "2.0", ID: got.ID, Result: json.RawMessage(`{"ok":true}`)})
	}()

	var out struct {
		OK bool `json:"ok"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.call(ctx, "test/method", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.OK {
		t.Fatal("result did not decode")
	}
}

func TestCallReportsRPCError(t *testing.T) {
	c, serverIn, serverOut := pipePair(t)
	go c.run()

	go func() {
		frame, _ := readFrame(serverIn)
		var got message
		_ = json.Unmarshal(frame, &got)
		serverSend(t, serverOut, &message{JSONRPC: "2.0", ID: got.ID, Error: &rpcError{Code: -32601, Message: "no such method"}})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.call(ctx, "test/method", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("err = %v, want the server's message", err)
	}
}

// TestCallFailsWhenServerDies is the reason the read loop closes every pending
// channel: without it a crashed server leaves the caller blocked until its
// context expires, which reads as a slow turn rather than a dead subprocess.
func TestCallFailsWhenServerDies(t *testing.T) {
	c, serverIn, serverOut := pipePair(t)
	go c.run()

	go func() {
		_, _ = readFrame(serverIn)
		serverOut.Close()
	}()

	// A context far longer than the test's patience: if the call waits for it,
	// the mechanism under test did nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.call(ctx, "test/method", nil, nil) }()

	select {
	case err := <-done:
		if !errors.Is(err, errServerGone) {
			t.Fatalf("err = %v, want errServerGone", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call hung after the server exited")
	}
}

func TestNotificationsReachHandler(t *testing.T) {
	c, _, serverOut := pipePair(t)
	got := make(chan string, 1)
	c.onNotify = func(method string, _ json.RawMessage) { got <- method }
	go c.run()

	serverSend(t, serverOut, &message{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: json.RawMessage(`{}`)})

	select {
	case m := <-got:
		if m != "textDocument/publishDiagnostics" {
			t.Fatalf("method = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never arrived")
	}
}

// TestServerRequestsAreAnswered pins that we reply to requests the server makes
// of us. gopls blocks on workspace/configuration during initialization, so a
// missing reply stops diagnostics with no error to explain it.
func TestServerRequestsAreAnswered(t *testing.T) {
	c, serverIn, serverOut := pipePair(t)
	go c.run()

	id := int64(7)
	serverSend(t, serverOut, &message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "workspace/configuration",
		Params:  json.RawMessage(`{"items":[{"section":"gopls"},{"section":"other"}]}`),
	})

	frame, err := readFrame(serverIn)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	var reply message
	if err := json.Unmarshal(frame, &reply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reply.ID == nil || *reply.ID != id {
		t.Fatalf("reply id = %v, want %d", reply.ID, id)
	}
	var settings []map[string]any
	if err := json.Unmarshal(reply.Result, &settings); err != nil {
		t.Fatalf("result is not a settings array: %v", err)
	}
	if len(settings) != 2 {
		t.Fatalf("got %d settings objects, want one per requested item", len(settings))
	}
}

func TestUnknownServerRequestStillGetsAReply(t *testing.T) {
	c, serverIn, serverOut := pipePair(t)
	go c.run()

	id := int64(3)
	serverSend(t, serverOut, &message{JSONRPC: "2.0", ID: &id, Method: "window/workDoneProgress/create", Params: json.RawMessage(`{}`)})

	frame, err := readFrame(serverIn)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	var reply message
	_ = json.Unmarshal(frame, &reply)
	if reply.ID == nil || *reply.ID != id {
		t.Fatalf("reply id = %v, want %d", reply.ID, id)
	}
}
