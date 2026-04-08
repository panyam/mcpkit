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
	"strconv"
	"strings"
	"sync"

	core "github.com/panyam/mcpkit/core"
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
	pending  sync.Map // id (string) → chan *core.Response
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
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(t.w, header); err != nil {
		return err
	}
	_, err := t.w.Write(data)
	return err
}

// readLoop reads Content-Length framed messages and routes them.
func (t *StdioTransport) readLoop() {
	defer close(t.done)

	for {
		data, err := readClientFrame(t.reader)
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
	if val, ok := t.pending.Load(idStr); ok {
		ch := val.(chan *core.Response)
		ch <- resp
	}
}

// readClientFrame reads a Content-Length framed message from a bufio.Reader.
// Same framing as the server side — shared protocol, separate implementation
// to keep the client package independent of the server package.
func readClientFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed header: %q", line)
		}
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if strings.EqualFold(name, "Content-Length") {
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q: %w", value, err)
			}
			contentLength = n
		}
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("reading body (%d bytes): %w", contentLength, err)
	}
	return body, nil
}
