package server

// Stdio transport for editor-spawned MCP servers (Cursor, Claude Desktop, etc.).
//
// Implements Content-Length framed JSON-RPC over stdin/stdout per the MCP spec
// (https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#stdio).
//
// Key spec requirements:
//   - Messages delimited by Content-Length header with \r\n\r\n separator
//   - Server MUST NOT write non-JSON-RPC data to stdout
//   - Server MAY write debug/log output to stderr
//   - Clean EOF handling on client disconnect
//
// Architecture: single session (stdio = 1 client = 1 connection). Uses the same
// dispatch path as HTTP transports: newSession() → dispatchWithNotifyAndRequest().
// Notifications and server-to-client requests (sampling, elicitation) are written
// to stdout as Content-Length framed messages, protected by a write mutex.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// StdioOption configures the stdio transport.
type StdioOption func(*stdioConfig)

type stdioConfig struct {
	input  io.Reader
	output io.Writer
	logger *log.Logger
}

// WithStdioInput overrides stdin for the stdio transport.
// Primarily used for testing with pipe pairs.
func WithStdioInput(r io.Reader) StdioOption {
	return func(c *stdioConfig) { c.input = r }
}

// WithStdioOutput overrides stdout for the stdio transport.
// Primarily used for testing with pipe pairs.
func WithStdioOutput(w io.Writer) StdioOption {
	return func(c *stdioConfig) { c.output = w }
}

// WithStdioLogger sets a logger for debug output on stderr.
// Debug logging is separate from the MCP protocol — it goes to stderr,
// never to stdout.
func WithStdioLogger(l *log.Logger) StdioOption {
	return func(c *stdioConfig) { c.logger = l }
}

// RunStdio runs the MCP server over stdio using Content-Length framed JSON-RPC.
// Blocks until ctx is cancelled or the input stream reaches EOF.
//
// This is the primary entry point for editor-spawned MCP servers. The server
// reads JSON-RPC messages from stdin (or the configured input), dispatches them
// through the standard Server dispatch pipeline, and writes responses to stdout
// (or the configured output).
//
// Example:
//
//	srv := server.NewServer(core.ServerInfo{Name: "my-server", Version: "1.0"})
//	srv.RegisterTool(def, handler)
//	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
//	defer cancel()
//	if err := srv.RunStdio(ctx); err != nil {
//	    log.Fatal(err)
//	}
func (s *Server) RunStdio(ctx context.Context, opts ...StdioOption) error {
	cfg := stdioConfig{
		input:  os.Stdin,
		output: os.Stdout,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	// Validate extension refs at startup (same as Handler() for HTTP transports).
	s.validateExtensionRefs()

	// Single session — stdio is always 1:1 client-to-server.
	dispatcher := s.newSession()
	dispatcher.sessionID = "stdio"
	defer dispatcher.Close()

	// Write mutex prevents interleaved writes from concurrent notifications
	// and responses on stdout.
	var writeMu sync.Mutex
	writer := cfg.output

	// writeFrameLocked writes a Content-Length framed message to stdout.
	writeFrameLocked := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return gohttp.WriteFrame(writer, data)
	}

	// Wire notifyFunc for server-to-client notifications (logging, progress, etc.).
	dispatcher.SetNotifyFunc(func(method string, params any) {
		raw, err := core.MarshalNotification(method, params)
		if err != nil {
			if cfg.logger != nil {
				cfg.logger.Printf("stdio: marshal notification %s: %v", method, err)
			}
			return
		}
		if err := writeFrameLocked(raw); err != nil {
			if cfg.logger != nil {
				cfg.logger.Printf("stdio: write notification: %v", err)
			}
		}
	})

	// Wire pushRequest for server-to-client requests (sampling, elicitation,
	// roots/list). Persistent for the lifetime of the stdio session.
	stdioPush := func(raw json.RawMessage) {
		if err := writeFrameLocked(raw); err != nil {
			if cfg.logger != nil {
				cfg.logger.Printf("stdio: write server request: %v", err)
			}
		}
	}
	dispatcher.SetPushRequest(stdioPush)

	// Build request func for server-to-client request/response matching.
	requestFunc := dispatcher.makeRequestFunc(stdioPush)

	reader := bufio.NewReader(cfg.input)

	// Use a channel to decouple the blocking read from context cancellation.
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)

	for {
		// Start a read in a goroutine so we can select on ctx.Done().
		go func() {
			data, err := gohttp.ReadFrame(reader)
			readCh <- readResult{data, err}
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-readCh:
			if result.err != nil {
				if result.err == io.EOF {
					// Client disconnected — clean shutdown.
					return nil
				}
				// Frame-level parse error: write a JSON-RPC error response.
				// Use null ID since we couldn't parse the request.
				errResp := core.NewErrorResponse(
					json.RawMessage("null"),
					core.ErrCodeParse,
					fmt.Sprintf("parse error: %v", result.err),
				)
				raw, _ := marshalJSON(errResp)
				if writeErr := writeFrameLocked(raw); writeErr != nil {
					return fmt.Errorf("stdio: write error response: %w", writeErr)
				}
				continue
			}

			// Detect if the incoming message is a response to a server-to-client request.
			if core.IsJSONRPCResponse(result.data) {
				var resp core.Response
				if err := json.Unmarshal(result.data, &resp); err == nil {
					dispatcher.RouteResponse(&resp)
				}
				continue
			}

			// Parse as a JSON-RPC request.
			var req core.Request
			if err := json.Unmarshal(result.data, &req); err != nil {
				errResp := core.NewErrorResponse(
					json.RawMessage("null"),
					core.ErrCodeParse,
					fmt.Sprintf("invalid JSON: %v", err),
				)
				raw, _ := marshalJSON(errResp)
				_ = writeFrameLocked(raw)
				continue
			}

			// Dispatch through the standard server pipeline.
			resp := s.dispatchWithNotifyAndRequest(
				dispatcher, ctx, nil,
				dispatcher.getNotifyFunc(), requestFunc, &req,
			)

			// Notifications produce no response.
			if resp == nil {
				continue
			}

			raw, err := marshalJSON(resp)
			if err != nil {
				if cfg.logger != nil {
					cfg.logger.Printf("stdio: marshal response: %v", err)
				}
				continue
			}
			if err := writeFrameLocked(raw); err != nil {
				return fmt.Errorf("stdio: write response: %w", err)
			}
		}
	}
}

