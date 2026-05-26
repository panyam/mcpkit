package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// initDraftSession bootstraps a Streamable HTTP session that negotiated
// the DRAFT-2026-v1 protocol version, so subsequent POSTs go through
// the SEP-2243 routing-header gate.
func initDraftSession(t *testing.T, url string) string {
	t.Helper()
	resp, err := streamablePost(url+"/mcp", "", &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"DRAFT-2026-v1","capabilities":{},"clientInfo":{"name":"draft-test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatalf("initialize POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	sessionID := resp.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("no Mcp-Session-Id header on initialize response")
	}
	// Notify initialized with matching Mcp-Method (gate is active on this session)
	req, _ := http.NewRequest(http.MethodPost, url+"/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(mcpSessionIDHeader, sessionID)
	req.Header.Set("Mcp-Method", "notifications/initialized")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialized notification failed: %v", err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusAccepted {
		t.Fatalf("initialized notification status = %d, want 202", r2.StatusCode)
	}
	return sessionID
}

func postWithHeaders(url, sessionID string, body any, extra map[string]string) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

func decodeJSONRPCError(t *testing.T, resp *http.Response) *core.Error {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed core.Response
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode response body %q: %v", string(body), err)
	}
	return parsed.Error
}

func TestHeaderValidation_DraftSession_RejectsMismatchedMethod(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()
	sessionID := initDraftSession(t, ts.URL)

	resp, err := postWithHeaders(ts.URL+"/mcp", sessionID,
		&core.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"},
		map[string]string{"Mcp-Method": "prompts/list"})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if e := decodeJSONRPCError(t, resp); e == nil || e.Code != core.ErrCodeHeaderMismatch {
		t.Errorf("error code = %+v, want -32001", e)
	}
}

func TestHeaderValidation_DraftSession_AcceptsMatchedHeaders(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()
	sessionID := initDraftSession(t, ts.URL)

	resp, err := postWithHeaders(ts.URL+"/mcp", sessionID,
		&core.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"},
		map[string]string{"Mcp-Method": "tools/list"})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestHeaderValidation_DatedSession_SkipsValidation(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()
	// streamableInit negotiates 2024-11-05 — outside the SEP-2243 enforced set.
	sessionID := streamableInit(t, ts.URL)

	// Missing Mcp-Method header on a tools/list call. Should NOT trigger
	// SEP-2243 validation because the negotiated version is dated. The
	// dispatcher returns a normal tools/list response.
	resp, err := streamablePost(ts.URL+"/mcp", sessionID,
		&core.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (gate should skip dated-version sessions); body: %s", resp.StatusCode, body)
	}
}

func TestHeaderValidation_DraftSession_MismatchedToolName(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()
	sessionID := initDraftSession(t, ts.URL)

	resp, err := postWithHeaders(ts.URL+"/mcp", sessionID,
		&core.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call",
			Params: json.RawMessage(`{"name":"echo","arguments":{"message":"hi"}}`)},
		map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "wrong_tool"})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if e := decodeJSONRPCError(t, resp); e == nil || e.Code != core.ErrCodeHeaderMismatch {
		t.Errorf("error code = %+v, want -32001", e)
	}
}
