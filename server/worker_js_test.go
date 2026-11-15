//go:build js && wasm

package server

import (
	"context"
	"encoding/json"
	"strings"
	"syscall/js"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// fakeWorker stands in for a Web Worker's globals under node: it captures
// postMessage calls and delivers page messages through globalThis.onmessage.
type fakeWorker struct {
	posted chan string
	post   js.Func
}

func newFakeWorker(t *testing.T) *fakeWorker {
	t.Helper()
	w := &fakeWorker{posted: make(chan string, 16)}
	w.post = js.FuncOf(func(_ js.Value, args []js.Value) any {
		w.posted <- args[0].String()
		return nil
	})
	js.Global().Set("postMessage", w.post)
	t.Cleanup(func() {
		js.Global().Delete("postMessage")
		w.post.Release()
	})
	return w
}

func (w *fakeWorker) send(t *testing.T, data any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for js.Global().Get("onmessage").Type() != js.TypeFunction {
		if time.Now().After(deadline) {
			t.Fatal("RunWorker never installed onmessage")
		}
		time.Sleep(5 * time.Millisecond)
	}
	evt := js.Global().Get("Object").New()
	evt.Set("data", data)
	js.Global().Get("onmessage").Invoke(evt)
}

func (w *fakeWorker) recv(t *testing.T) map[string]any {
	t.Helper()
	select {
	case line := <-w.posted:
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("posted message is not JSON: %q: %v", line, err)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no postMessage within 5s")
		return nil
	}
}

func TestRunWorkerServesOverPostMessage(t *testing.T) {
	w := newFakeWorker(t)
	srv := NewServer(core.ServerInfo{Name: "worker-test", Version: "1"})
	srv.RegisterTool(core.ToolDef{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("pong"), nil
		})

	if runOverride == nil {
		t.Fatal("js/wasm build: Run should delegate to RunWorker")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.RunWorker(ctx) }()

	// A pretty-printed string must still arrive as one message.
	w.send(t, "{\n  \"jsonrpc\": \"2.0\", \"id\": 1, \"method\": \"initialize\",\n"+
		"  \"params\": {\"protocolVersion\": \"2025-11-25\", \"capabilities\": {},"+
		" \"clientInfo\": {\"name\": \"t\", \"version\": \"1\"}}\n}")
	initResp := w.recv(t)
	if initResp["id"] != float64(1) || initResp["result"] == nil {
		t.Fatalf("initialize response = %v", initResp)
	}
	w.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// A plain object goes through JSON.stringify.
	call := js.Global().Get("JSON").Call("parse",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	w.send(t, call)
	resp := w.recv(t)
	if resp["id"] != float64(2) {
		t.Fatalf("tools/call response id = %v, want 2", resp["id"])
	}
	if !strings.Contains(mustJSON(t, resp["result"]), `"pong"`) {
		t.Fatalf("tools/call result = %v, want text pong", resp["result"])
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunWorker did not return after cancel")
	}
	if js.Global().Get("onmessage").Type() == js.TypeFunction {
		t.Fatal("RunWorker left onmessage installed")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
