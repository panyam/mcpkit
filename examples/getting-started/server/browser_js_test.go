//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"
	"testing"
	"time"
)

// TestBrowserBuild drives this example's main path, newServer then Run, the
// way ../web/worker.js does: JSON-RPC strings in through onmessage, out
// through postMessage. Node stands in for the Web Worker.
func TestBrowserBuild(t *testing.T) {
	posted := make(chan string, 16)
	post := js.FuncOf(func(_ js.Value, args []js.Value) any {
		posted <- args[0].String()
		return nil
	})
	js.Global().Set("postMessage", post)

	// Run never returns in a wasm build. The test binary exits when the
	// test does.
	go newServer().Run(":8787")

	send := func(msg string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for js.Global().Get("onmessage").Type() != js.TypeFunction {
			if time.Now().After(deadline) {
				t.Fatal("Run never installed onmessage")
			}
			time.Sleep(5 * time.Millisecond)
		}
		evt := js.Global().Get("Object").New()
		evt.Set("data", msg)
		js.Global().Get("onmessage").Invoke(evt)
	}
	recv := func() map[string]any {
		t.Helper()
		select {
		case line := <-posted:
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("posted message is not JSON: %q", line)
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no postMessage within 5s")
			return nil
		}
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if r := recv(); r["id"] != float64(1) || r["result"] == nil {
		t.Fatalf("initialize = %v", r)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"greet","arguments":{"name":"world"}}}`)
	r := recv()
	content, _ := r["result"].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "Hello, world!" {
		t.Fatalf("greet = %v, want text Hello, world!", r)
	}
}
