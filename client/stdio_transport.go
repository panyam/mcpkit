package client

// Stdio client transport for Content-Length framed JSON-RPC over reader/writer pairs.
//
// Implements core.Transport so it can be used with client.WithTransport().
// Designed for editor-spawned MCP servers where the client communicates with
// a child process over stdin/stdout pipes.
//
// The transport runs a background read loop that:
//   - Routes JSON-RPC responses to pending Call() waiters
//   - Dispatches server-to-client requests (sampling, elicitation) to the handler
//   - Delivers notifications to the notification handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	conc "github.com/panyam/gocurrent"
	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// StdioTransport implements core.Transport over Content-Length framed JSON-RPC.
// Messages are read from r and written to w using the same framing as the
// MCP stdio server transport (Content-Length: N\r\n\r\n<body>).
type StdioTransport struct {
	r io.Reader
	w io.Writer

	// Handlers set by the client before Connect().
	serverReqHandler core.ServerRequestHandler
	notifyHandler    core.NotificationHandler

	// Internal state.
	reader   *bufio.Reader
	writeMu  sync.Mutex
	pending  conc.SyncMap[string, chan *core.Response]
	done     chan struct{}
	closeErr error
}

// NewStdioTransport creates a client transport that communicates via
// Content-Length framed JSON-RPC over the given reader/writer pair.
//
// Typically the reader is connected to the server's stdout and the writer
// to the server's stdin (or pipe ends in tests).
//
// Example:
//
//	cmd := exec.Command("my-mcp-server")
//	stdin, _ := cmd.StdinPipe()
//	stdout, _ := cmd.StdoutPipe()
//	cmd.Start()
//	transport := client.NewStdioTransport(stdout, stdin)
//	c := client.NewClient("stdio://", info, client.WithTransport(transport))
func NewStdioTransport(r io.Reader, w io.Writer) *StdioTransport {
	return &StdioTransport{r: r, w: w}
}

// WithStdioTransport configures a Client to use stdio transport over the given
// reader/writer pair. Unlike WithTransport(NewStdioTransport(...)), this option
// wires the client's sampling/elicitation handlers and notification callback into
// the stdio transport automatically.
func WithStdioTransport(r io.Reader, w io.Writer) ClientOption {
	return func(c *Client) {
		c.stdioReader = r
		c.stdioWriter = w
	}
}

// Connect starts the background read loop.
func (t *StdioTransport) Connect(ctx context.Context) error {
	t.reader = bufio.NewReader(t.r)
	t.done = make(chan struct{})
	go t.readLoop()
	return nil
}

// Call sends a JSON-RPC request and waits for the matching response.
func (t *StdioTransport) Call(ctx context.Context, req *core.Request) (*core.Response, error) {
	// Extract request ID as string key for pending map.
	var idStr string
	if req.ID != nil {
		if err := json.Unmarshal(req.ID, &idStr); err != nil {
			// Numeric or other ID — use raw string.
			idStr = string(req.ID)
		}
	}

	ch := make(chan *core.Response, 1)
	t.pending.Store(idStr, ch)
	defer t.pending.Delete(idStr)

	// Marshal and send.
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if err := t.writeFrame(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Wait for response or cancellation.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		return resp, nil
	case <-t.done:
		return nil, fmt.Errorf("stdio transport closed")
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (t *StdioTransport) Notify(ctx context.Context, req *core.Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	return t.writeFrame(data)
}

// Close shuts down the transport and waits for the read loop to exit.
func (t *StdioTransport) Close() error {
	// Close the writer to signal EOF to the server.
	if closer, ok := t.w.(io.Closer); ok {
		closer.Close()
	}
	// Wait for read loop to finish (it will hit EOF or read error).
	if t.done != nil {
		<-t.done
	}
	return t.closeErr
}

// SessionID returns "stdio" for the stdio transport.
func (t *StdioTransport) SessionID() string { return "stdio" }

// writeFrame writes a Content-Length framed message, protected by mutex.
func (t *StdioTransport) writeFrame(data []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return gohttp.WriteFrame(t.w, data)
}

// readLoop reads Content-Length framed messages and routes them.
func (t *StdioTransport) readLoop() {
	defer close(t.done)

	for {
		data, err := gohttp.ReadFrame(t.reader)
		if err != nil {
			if err != io.EOF {
				t.closeErr = err
			}
			return
		}

		// Detect message type.
		if core.IsJSONRPCResponse(data) {
			// Response to a pending Call().
			var resp core.Response
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}
			t.routeResponse(&resp)
			continue
		}

		// It's a request or notification from the server.
		var req core.Request
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}

		if req.IsNotification() {
			// Server-to-client notification.
			if t.notifyHandler != nil {
				t.notifyHandler(req.Method, req.Params)
			}
			continue
		}

		// Server-to-client request (sampling, elicitation).
		if t.serverReqHandler != nil {
			resp := t.serverReqHandler(context.Background(), &req)
			if resp != nil {
				raw, err := json.Marshal(resp)
				if err != nil {
					continue
				}
				_ = t.writeFrame(raw)
			}
		}
	}
}

// routeResponse delivers a response to the waiting Call() goroutine.
func (t *StdioTransport) routeResponse(resp *core.Response) {
	if resp.ID == nil {
		return
	}
	var idStr string
	if err := json.Unmarshal(resp.ID, &idStr); err != nil {
		idStr = string(resp.ID)
	}
	if ch, ok := t.pending.Load(idStr); ok {
		ch <- resp
	}
}

