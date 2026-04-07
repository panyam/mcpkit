package client

// Client-side transport logging. Wraps a clientTransport to log every
// connect, call, notify, and close operation with method name, latency,
// and error details. Enable via WithClientLogging(logger).

import (
	"encoding/json"
	"log"
	"time"
)

// WithClientLogging enables debug logging of all client transport operations.
// Every connect, call, notify, and close is logged with method name, latency,
// and error details. Pass nil to use the default logger.
//
// Example:
//
//	client := mcpkit.NewClient(url, info,
//	    mcpkit.WithClientLogging(log.Default()),
//	)
func WithClientLogging(logger *log.Logger) ClientOption {
	return func(c *Client) {
		if logger == nil {
			logger = log.Default()
		}
		c.logger = logger
	}
}

// loggingTransport wraps a clientTransport with debug logging.
type loggingTransport struct {
	inner  clientTransport
	logger *log.Logger
}

// connect logs the connection attempt with duration and success/failure.
func (t *loggingTransport) connect() error {
	start := time.Now()
	err := t.inner.connect()
	elapsed := time.Since(start)
	if err != nil {
		t.logger.Printf("[mcpkit] connect error=%v [%s]", err, elapsed)
	} else {
		sid := t.inner.getSessionID()
		if sid != "" {
			t.logger.Printf("[mcpkit] connect ok session=%s [%s]", sid, elapsed)
		} else {
			t.logger.Printf("[mcpkit] connect ok [%s]", elapsed)
		}
	}
	return err
}

// call logs the JSON-RPC method name, latency, and result status.
func (t *loggingTransport) call(data []byte) (*rpcResponse, error) {
	method := extractMethodFromJSON(data)
	start := time.Now()
	resp, err := t.inner.call(data)
	elapsed := time.Since(start)
	if err != nil {
		t.logger.Printf("[mcpkit] → %s error=%v [%s]", method, err, elapsed)
	} else if resp != nil && resp.Error != nil {
		t.logger.Printf("[mcpkit] → %s rpc_error=%d (%s) [%s]",
			method, resp.Error.Code, resp.Error.Message, elapsed)
	} else {
		t.logger.Printf("[mcpkit] → %s ok [%s]", method, elapsed)
	}
	return resp, err
}

// notify logs the JSON-RPC notification method and any error.
func (t *loggingTransport) notify(data []byte) error {
	method := extractMethodFromJSON(data)
	start := time.Now()
	err := t.inner.notify(data)
	elapsed := time.Since(start)
	if err != nil {
		t.logger.Printf("[mcpkit] → %s (notify) error=%v [%s]", method, err, elapsed)
	} else {
		t.logger.Printf("[mcpkit] → %s (notify) ok [%s]", method, elapsed)
	}
	return err
}

func (t *loggingTransport) close() error {
	err := t.inner.close()
	if err != nil {
		t.logger.Printf("[mcpkit] close error=%v", err)
	} else {
		t.logger.Printf("[mcpkit] close ok")
	}
	return err
}

func (t *loggingTransport) getSessionID() string {
	return t.inner.getSessionID()
}

// extractMethodFromJSON extracts the "method" field from a JSON-RPC envelope
// without full deserialization. Returns "<unknown>" if extraction fails.
func extractMethodFromJSON(data []byte) string {
	var envelope struct {
		Method string `json:"method"`
	}
	json.Unmarshal(data, &envelope) // ignore error; defaults to ""
	if envelope.Method == "" {
		return "<unknown>"
	}
	return envelope.Method
}
