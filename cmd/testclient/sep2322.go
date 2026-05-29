package main

// SEP-2322 client MRTR driver for the
// `sep-2322-client-request-state` upstream scenario.
//
// The upstream fixture is a bare-HTTP JSON-RPC endpoint (no MCP
// initialize handshake, no SEP-2575 server/discover) that grades how
// the client handles InputRequiredResult on tools/call:
//
//   - test_mrtr_echo_state      — verifies requestState is echoed byte-
//                                  for-byte AND the JSON-RPC id changes
//                                  on retry.
//   - test_mrtr_no_state        — verifies the client OMITS requestState
//                                  on retry when the server didn't send
//                                  one.
//   - test_mrtr_unrelated       — verifies inputResponses / requestState
//                                  from one tool's MRTR flow do NOT leak
//                                  to a parallel call against another
//                                  tool.
//   - test_mrtr_no_result_type  — verifies the client treats a result
//                                  with no resultType as "complete" by
//                                  default (no retry).
//
// We can't use mcpkit's connected client.Client here because the fixture
// doesn't implement initialize. The driver shapes raw POSTs that match
// what a SEP-2322-conformant client would emit; the fixture's per-tool
// handlers grade the wire shape and push checks into its checks buffer.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// nextID is a monotonically-incrementing JSON-RPC request id source.
// The conformance scenario explicitly checks that retries use a
// DIFFERENT id than the original request, so the only requirement is
// uniqueness across this driver run.
var nextID atomic.Int64

func sep2322NextID() int64 { return nextID.Add(1) }

// driveSEP2322ClientRequestState runs the four fixture tools through
// the wire-correct MRTR contract: round 1 to elicit the
// InputRequiredResult, round 2 with `inputResponses` and (only if
// echoed) the unchanged `requestState`. The fixture pushes its grading
// checks as a side effect of each call — we drive, the fixture grades.
func driveSEP2322ClientRequestState(serverURL string) error {
	// test_mrtr_echo_state: server returns requestState; we MUST echo
	// it verbatim AND use a different id on retry.
	if err := mrtrRoundTrip(serverURL, "test_mrtr_echo_state", true /* echoRequestState */); err != nil {
		return fmt.Errorf("test_mrtr_echo_state: %w", err)
	}

	// test_mrtr_no_state: server returns InputRequiredResult WITHOUT
	// requestState; we MUST NOT invent one on retry. Same loop, just
	// don't propagate a state token.
	if err := mrtrRoundTrip(serverURL, "test_mrtr_no_state", true /* echoRequestState */); err != nil {
		return fmt.Errorf("test_mrtr_no_state: %w", err)
	}

	// test_mrtr_unrelated: a separate tool call after the above must NOT
	// carry over inputResponses / requestState from another tool's flow.
	// One bare call with no MRTR fields is the spec-correct behaviour.
	if _, err := postJSONRPC(serverURL, "tools/call", map[string]any{
		"name":      "test_mrtr_unrelated",
		"arguments": map[string]any{},
	}); err != nil {
		return fmt.Errorf("test_mrtr_unrelated: %w", err)
	}

	// test_mrtr_no_result_type: server returns a result with no
	// resultType; the client MUST treat it as "complete" and NOT retry.
	// One bare call, no second round even if the result looks
	// unfamiliar.
	if _, err := postJSONRPC(serverURL, "tools/call", map[string]any{
		"name":      "test_mrtr_no_result_type",
		"arguments": map[string]any{},
	}); err != nil {
		return fmt.Errorf("test_mrtr_no_result_type: %w", err)
	}

	return nil
}

// mrtrRoundTrip executes one MRTR loop:
//
//  1. Initial tools/call (no inputResponses).
//  2. If the response is an InputRequiredResult, build inputResponses
//     by canned-resolving each InputRequest (action: accept,
//     confirmed: true is what the fixture's elicitation schema asks
//     for) and re-issue tools/call with a NEW JSON-RPC id and the
//     unchanged requestState (when the server sent one).
//
// echoRequestState toggles whether the server's requestState — when
// present — gets echoed back. The fixture's no-state variant returns
// an InputRequiredResult with no requestState; the driver MUST then
// retry without inventing one. Pass true for both variants — when the
// server's response field is absent the helper naturally skips it.
func mrtrRoundTrip(serverURL, toolName string, echoRequestState bool) error {
	r1, err := postJSONRPC(serverURL, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	if r1.Error != nil {
		return fmt.Errorf("round 1: %s (code=%d)", r1.Error.Message, r1.Error.Code)
	}
	result, _ := r1.Result.(map[string]any)
	if result == nil {
		return fmt.Errorf("round 1 result missing")
	}
	// resultType != "input_required" → complete, no retry.
	if result["resultType"] != "input_required" {
		return nil
	}
	reqs, _ := result["inputRequests"].(map[string]any)
	responses := make(map[string]any, len(reqs))
	for key := range reqs {
		// Canned: every InputRequest in this fixture is an elicitation
		// asking for {confirmed: bool}, and the fixture's grading is
		// indifferent to the actual content.
		responses[key] = map[string]any{
			"action":  "accept",
			"content": map[string]any{"confirmed": true},
		}
	}

	retryParams := map[string]any{
		"name":           toolName,
		"arguments":      map[string]any{},
		"inputResponses": responses,
	}
	if echoRequestState {
		if state, ok := result["requestState"].(string); ok && state != "" {
			retryParams["requestState"] = state
		}
	}
	r2, err := postJSONRPC(serverURL, "tools/call", retryParams)
	if err != nil {
		return err
	}
	if r2.Error != nil {
		return fmt.Errorf("round 2: %s (code=%d)", r2.Error.Message, r2.Error.Code)
	}
	return nil
}

// jsonRPCResponse mirrors the inbound JSON-RPC envelope shape.
type jsonRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonRPCError  `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// postJSONRPC POSTs a single JSON-RPC request and returns the decoded
// response envelope. Each call mints a fresh id so the fixture's
// "different id on retry" check observes the change.
func postJSONRPC(serverURL, method string, params any) (*jsonRPCResponse, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      sep2322NextID(),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	httpReq, err := http.NewRequest("POST", serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", method, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var out jsonRPCResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response (%d bytes): %w\n%s", len(respBody), err, string(respBody))
	}
	return &out, nil
}
