package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// SEP-2322 input_required on prompts/get.
//
// The upstream conformance scenario `input-required-result-non-tool-request`
// drives a prompts/get call against a fixture prompt that returns an
// InputRequiredResult on round 1 and a complete GetPromptResult on round 2
// (after the client echoes inputResponses). Both wires must support the
// flow.

// registerInputRequiredPrompt registers a prompt that asks for one
// elicitation input on the first call and returns a greeting on the
// second.
func registerInputRequiredPrompt(t *testing.T, s *Server) {
	t.Helper()
	if err := s.Registry().AddPrompt(
		core.PromptDef{
			Name:        "test_input_required_result_prompt",
			Description: "Prompts the client for a context value, then builds a templated prompt around it.",
		},
		func(ctx core.PromptContext, _ core.PromptRequest) (core.PromptResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_context": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What context should the prompt use?","requestedSchema":{"type":"object","properties":{"context":{"type":"string"}},"required":["context"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_context")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Context string `json:"context"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.PromptResult{}, nil
			}
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Context: " + er.Content.Context},
				}},
			}, nil
		},
	); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
}

// TestPromptsGet_StatelessMRTR_RoundTrip drives the upstream A9 shape over
// the stateless wire: round 1 returns InputRequiredResult with non-empty
// requestState; round 2 (same prompt, with inputResponses + the echoed
// requestState) returns a complete GetPromptResult.
func TestPromptsGet_StatelessMRTR_RoundTrip(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "prompts-mrtr-test", Version: "0.0.1"})
	registerInputRequiredPrompt(t, s)

	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeDual))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	postPromptsGet := func(extra map[string]any) *core.Response {
		params := map[string]any{
			"name": "test_input_required_result_prompt",
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{"elicitation": map[string]any{}},
			},
		}
		for k, v := range extra {
			params[k] = v
		}
		resp := postStatelessJSON(t, ts.URL+"/mcp", map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "prompts/get",
			"params": params,
		}, map[string]string{mcpProtocolVersionHeader: draftVersion})
		return decode(t, resp)
	}

	r1 := postPromptsGet(nil)
	if r1.Error != nil {
		t.Fatalf("round 1 error: %+v", r1.Error)
	}
	m1, _ := r1.Result.(map[string]any)
	if got := m1["resultType"]; got != "input_required" {
		t.Fatalf("round 1 resultType = %v, want input_required", got)
	}
	reqs, ok := m1["inputRequests"].(map[string]any)
	if !ok || reqs["user_context"] == nil {
		t.Fatalf("round 1 inputRequests missing user_context: %v", m1)
	}
	state1, _ := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("round 1 requestState empty (stateless wire dropped it)")
	}

	r2 := postPromptsGet(map[string]any{
		"inputResponses": map[string]any{
			"user_context": map[string]any{"action": "accept", "content": map[string]any{"context": "demo"}},
		},
		"requestState": state1,
	})
	if r2.Error != nil {
		t.Fatalf("round 2 error: %+v", r2.Error)
	}
	m2, _ := r2.Result.(map[string]any)
	if m2["resultType"] == "input_required" {
		t.Fatalf("round 2 still input_required")
	}
	msgs, _ := m2["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("round 2 missing messages: %v", m2)
	}
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	text, _ := content["text"].(string)
	if !strings.Contains(text, "demo") {
		t.Errorf("round 2 text = %q, want substring \"demo\"", text)
	}
}

// TestPromptsGet_LegacyMRTR_RoundTrip exercises the same A9 shape on the
// legacy wire (initialize handshake + Mcp-Session-Id) so the new field on
// PromptResult and the dispatch type-switch on PromptResponse are both
// covered end-to-end.
func TestPromptsGet_LegacyMRTR_RoundTrip(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "prompts-mrtr-test", Version: "0.0.1"})
	registerInputRequiredPrompt(t, s)

	c := connectMRTRClient(t, s)

	r1, err := c.Call("prompts/get", map[string]any{"name": "test_input_required_result_prompt"})
	if err != nil {
		t.Fatalf("round 1 call: %v", err)
	}
	var m1 map[string]any
	if err := json.Unmarshal(r1.Raw, &m1); err != nil {
		t.Fatalf("decode r1: %v", err)
	}
	if m1["resultType"] != "input_required" {
		t.Fatalf("round 1 resultType = %v, want input_required; raw=%s", m1["resultType"], r1.Raw)
	}
	state1, _ := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("round 1 requestState empty; raw=%s", r1.Raw)
	}

	r2, err := c.Call("prompts/get", map[string]any{
		"name": "test_input_required_result_prompt",
		"inputResponses": map[string]any{
			"user_context": map[string]any{"action": "accept", "content": map[string]any{"context": "demo"}},
		},
		"requestState": state1,
	})
	if err != nil {
		t.Fatalf("round 2 call: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(r2.Raw, &m2); err != nil {
		t.Fatalf("decode r2: %v", err)
	}
	if m2["resultType"] == "input_required" {
		t.Fatalf("round 2 still input_required; raw=%s", r2.Raw)
	}
	msgs, _ := m2["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("round 2 missing messages; raw=%s", r2.Raw)
	}
}
