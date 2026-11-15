//go:build js && wasm

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"syscall/js"
)

func init() { runOverride = func(s *Server) error { return s.RunWorker(context.Background()) } }

// workerInboxSize bounds the messages queued between the onmessage callback
// and the server's reader. The callback can't block on the pipe, because a
// blocked JS callback stalls the event loop and the server would never get
// to read (#1711).
const workerInboxSize = 1024

// RunWorker serves the MCP session to the page that started this Web Worker.
// It exists only in js/wasm builds. Each MCP message is one postMessage in
// each direction, carrying one JSON-RPC message as a string. The page may
// also post a plain object, which is sent through JSON.stringify first.
//
// It installs globalThis.onmessage, so the worker can host one session.
// Under js/wasm, Run calls RunWorker and ignores its address and transport
// options, because a browser has no socket to listen on. Features that need
// HTTP (auth, Origin checks, SSE, webhooks) aren't available here.
//
// RunWorker blocks until ctx is cancelled.
func (s *Server) RunWorker(ctx context.Context) error {
	global := js.Global()
	pr, pw := io.Pipe()
	inbox := make(chan []byte, workerInboxSize)

	onMessage := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		data := args[0].Get("data")
		var raw string
		if data.Type() == js.TypeString {
			raw = data.String()
		} else {
			raw = global.Get("JSON").Call("stringify", data).String()
		}
		// Newline-delimited framing can't carry an embedded newline, and a
		// page may post pretty-printed JSON.
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(raw)); err != nil {
			buf.Reset()
			buf.WriteString(raw)
		}
		buf.WriteByte('\n')
		select {
		case inbox <- buf.Bytes():
		default:
			global.Get("console").Call("error", "mcpkit worker: inbox full, dropping message")
		}
		return nil
	})
	defer onMessage.Release()
	global.Set("onmessage", onMessage)

	safeGo("worker.inbox", func() {
		for {
			select {
			case <-ctx.Done():
				pw.Close()
				return
			case msg := <-inbox:
				if _, err := pw.Write(msg); err != nil {
					return
				}
			}
		}
	})

	out := &postMessageWriter{post: global.Get("postMessage")}
	err := s.RunIO(ctx, pr, out, WithStdioFraming(FramingNewlineDelimited))
	global.Delete("onmessage")
	return err
}

// postMessageWriter turns the stdio transport's newline-delimited output
// into one postMessage call per message.
type postMessageWriter struct {
	mu   sync.Mutex
	post js.Value
	buf  []byte
}

func (w *postMessageWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.post.Invoke(line)
		}
	}
}
