package server

import (
	"encoding/json"
	core "github.com/panyam/mcpkit/core"
	"io"
	"testing"
)

func TestDebugSSE(t *testing.T) {
	ts := testStreamableServerWithLogging()
	defer ts.Close()

	sessionID := streamableInitWithLogging(t, ts.URL)

	resp, err := streamablePostSSE(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"log_tool","arguments":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("Status: %d, Content-Type: %s, Body: %s", resp.StatusCode, resp.Header.Get("Content-Type"), string(body))
}
