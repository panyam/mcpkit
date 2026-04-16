package core

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// LoggingTransport is a Transport decorator that logs every JSON-RPC message
// flowing through the transport. Use it for wire-level debugging, conformance
// testing, and audit logging.
//
// Wraps any Transport — zero cost when not used. Complements server middleware
// (which operates at the method level, post-deserialization) by providing
// raw message visibility before parsing.
//
// Example:
//
//	inner := server.NewInProcessTransport(srv)
//	logged := &core.LoggingTransport{Inner: inner, Logger: log.Default()}
//	c := client.NewClient("", info, client.WithTransport(logged))
type LoggingTransport struct {
	// Inner is the wrapped transport.
	Inner Transport

	// Logger receives log output. If nil, log.Default() is used.
	Logger *log.Logger

	// LogBodies controls whether full JSON-RPC message bodies are included
	// in log output. When false (default), only method names and direction
	// are logged — suitable for production. When true, full request/response
	// JSON is logged — useful for debugging but verbose.
	LogBodies bool
}

func (t *LoggingTransport) logger() *log.Logger {
	if t.Logger != nil {
		return t.Logger
	}
	return log.Default()
}

// Connect delegates to the inner transport and logs the result.
func (t *LoggingTransport) Connect(ctx context.Context) error {
	err := t.Inner.Connect(ctx)
	if err != nil {
		t.logger().Printf("[mcp] connect error: %v", err)
	} else {
		t.logger().Printf("[mcp] connected (session=%s)", t.Inner.SessionID())
	}
	return err
}

// Call delegates to the inner transport and logs the request, response, and latency.
func (t *LoggingTransport) Call(ctx context.Context, req *Request) (*Response, error) {
	l := t.logger()
	if t.LogBodies {
		body, _ := json.Marshal(req)
		l.Printf("[mcp] → %s %s", req.Method, body)
	} else {
		l.Printf("[mcp] → %s", req.Method)
	}

	start := time.Now()
	resp, err := t.Inner.Call(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		l.Printf("[mcp] ← %s error (%s): %v", req.Method, elapsed.Round(time.Millisecond), err)
	} else if resp != nil && resp.Error != nil {
		l.Printf("[mcp] ← %s rpc-error (%s): [%d] %s", req.Method, elapsed.Round(time.Millisecond), resp.Error.Code, resp.Error.Message)
	} else {
		if t.LogBodies && resp != nil {
			body, _ := json.Marshal(resp)
			l.Printf("[mcp] ← %s ok (%s) %s", req.Method, elapsed.Round(time.Millisecond), body)
		} else {
			l.Printf("[mcp] ← %s ok (%s)", req.Method, elapsed.Round(time.Millisecond))
		}
	}
	return resp, err
}

// Notify delegates to the inner transport and logs the notification.
func (t *LoggingTransport) Notify(ctx context.Context, req *Request) error {
	l := t.logger()
	if t.LogBodies {
		body, _ := json.Marshal(req)
		l.Printf("[mcp] → notify %s %s", req.Method, body)
	} else {
		l.Printf("[mcp] → notify %s", req.Method)
	}
	return t.Inner.Notify(ctx, req)
}

// Close delegates to the inner transport and logs the close.
func (t *LoggingTransport) Close() error {
	t.logger().Printf("[mcp] closing transport")
	return t.Inner.Close()
}

// SessionID delegates to the inner transport.
func (t *LoggingTransport) SessionID() string {
	return t.Inner.SessionID()
}
