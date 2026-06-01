package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestResultTypeConstants verifies the wire values of the ResultType
// discriminator. "task" is SEP-2557; "complete" / "input_required" are
// SEP-2322 (MRTR), with "input_required" renamed from the earlier
// "incomplete" in commit de6d76fb (merged 2026-05-06).
func TestResultTypeConstants(t *testing.T) {
	cases := []struct {
		rt   ResultType
		want string
	}{
		{ResultTypeTask, "task"},
		{ResultTypeComplete, "complete"},
		{ResultTypeInputRequired, "input_required"},
	}
	for _, tc := range cases {
		if string(tc.rt) != tc.want {
			t.Errorf("ResultType(%v) = %q, want %q", tc.rt, string(tc.rt), tc.want)
		}
		// JSON round-trip as a bare string.
		data, err := json.Marshal(tc.rt)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", tc.rt, err)
		}
		var got ResultType
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got != tc.rt {
			t.Errorf("round-trip: got %q, want %q", got, tc.rt)
		}
	}
}

func TestDefaultResultType(t *testing.T) {
	t.Run("empty-gets-default", func(t *testing.T) {
		var rt ResultType
		defaultResultType(&rt, ResultTypeComplete)
		if rt != ResultTypeComplete {
			t.Errorf("empty → %q, want %q", rt, ResultTypeComplete)
		}
	})
	t.Run("non-empty-preserved", func(t *testing.T) {
		rt := ResultTypeTask
		defaultResultType(&rt, ResultTypeComplete)
		if rt != ResultTypeTask {
			t.Errorf("preset %q clobbered to %q", ResultTypeTask, rt)
		}
	})
}

// TestInputRequestJSON verifies wire shape: {"method": "...", "params": {...}}
// with params omitted when nil. Round-trip must preserve raw params bytes.
func TestInputRequestJSON(t *testing.T) {
	params := json.RawMessage(`{"message":"Confirm?","schema":{"type":"object"}}`)
	req := InputRequest{
		Method: "elicitation/create",
		Params: params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["method"] != "elicitation/create" {
		t.Errorf("method = %v, want elicitation/create", m["method"])
	}
	if _, ok := m["params"]; !ok {
		t.Error("params missing from JSON output")
	}

	var decoded InputRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Method != req.Method {
		t.Errorf("Method = %q, want %q", decoded.Method, req.Method)
	}
	// json.RawMessage round-trips as raw bytes; compact equivalence is enough
	// because the encoder may re-format whitespace.
	var orig, got any
	_ = json.Unmarshal(params, &orig)
	_ = json.Unmarshal(decoded.Params, &got)
	origBytes, _ := json.Marshal(orig)
	gotBytes, _ := json.Marshal(got)
	if string(origBytes) != string(gotBytes) {
		t.Errorf("Params mismatch: got %s, want %s", gotBytes, origBytes)
	}
}

// TestInputRequestOmitEmptyParams verifies that an InputRequest with nil
// params omits the field entirely (not "params":null).
func TestInputRequestOmitEmptyParams(t *testing.T) {
	req := InputRequest{Method: "ping"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["params"]; ok {
		t.Errorf("params should be omitted when nil; got %s", data)
	}
}

// TestInputRequestsMapJSON verifies that InputRequests (alias for
// map[string]InputRequest) marshals as a JSON object keyed by request id.
func TestInputRequestsMapJSON(t *testing.T) {
	reqs := InputRequests{
		"elicit-1": InputRequest{Method: "elicitation/create", Params: json.RawMessage(`{"message":"ok?"}`)},
		"sample-1": InputRequest{Method: "sampling/createMessage", Params: json.RawMessage(`{"maxTokens":50}`)},
	}
	data, err := json.Marshal(reqs)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InputRequests
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, want 2", len(decoded))
	}
	if decoded["elicit-1"].Method != "elicitation/create" {
		t.Errorf("elicit-1.method = %q, want elicitation/create", decoded["elicit-1"].Method)
	}
	if decoded["sample-1"].Method != "sampling/createMessage" {
		t.Errorf("sample-1.method = %q, want sampling/createMessage", decoded["sample-1"].Method)
	}
}

// TestInputResponsesMapJSON verifies that InputResponses (alias for
// map[string]json.RawMessage) preserves opaque payload bytes per key.
func TestInputResponsesMapJSON(t *testing.T) {
	resps := InputResponses{
		"elicit-1": json.RawMessage(`{"action":"accept","content":{"confirmed":true}}`),
		"sample-1": json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"hello"}}`),
	}
	data, err := json.Marshal(resps)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InputResponses
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, want 2", len(decoded))
	}
	var v map[string]any
	if err := json.Unmarshal(decoded["elicit-1"], &v); err != nil {
		t.Fatalf("elicit-1 not valid JSON: %v", err)
	}
	if v["action"] != "accept" {
		t.Errorf("elicit-1.action = %v, want accept", v["action"])
	}
}

// TestInputRequiredResultWireShape verifies the SEP-2322 ephemeral wire
// shape: {"resultType": "input_required", "inputRequests": {...},
// "requestState": "..."}. resultType is camelCase like every other MCP
// wire field — Luca confirmed camelCase is the SEP-2322 spec standard.
// "input_required" is the SEP-2322 wire variant value, renamed from
// "incomplete" in commit de6d76fb (merged 2026-05-06).
func TestInputRequiredResultWireShape(t *testing.T) {
	res := InputRequiredResult{
		InputRequests: InputRequests{
			"user_name": InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?"}`),
			},
		},
		RequestState: "opaque-state",
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["resultType"] != "input_required" {
		t.Errorf("resultType = %v, want \"input_required\"; got %s", m["resultType"], data)
	}
	if _, ok := m["result_type"]; ok {
		t.Errorf("snake_case result_type must NOT appear (camelCase is the wire field); got %s", data)
	}
	if m["requestState"] != "opaque-state" {
		t.Errorf("requestState = %v, want opaque-state", m["requestState"])
	}
	reqs, ok := m["inputRequests"].(map[string]any)
	if !ok {
		t.Fatalf("inputRequests missing or wrong shape; got %s", data)
	}
	entry, ok := reqs["user_name"].(map[string]any)
	if !ok {
		t.Fatalf("inputRequests[user_name] missing; got %s", data)
	}
	if entry["method"] != "elicitation/create" {
		t.Errorf("inputRequests[user_name].method = %v, want elicitation/create", entry["method"])
	}
}

// TestInputRequiredResult_SatisfiesBothResponseInterfaces guards the
// sealed-interface marker methods: SEP-2322 InputRequiredResult is a
// variant of BOTH ToolResponse (tools/call) AND PromptResponse
// (prompts/get, per upstream's input-required-result-non-tool-request
// scenario). Compile-time check via interface assertion — if either
// marker is dropped, this test stops building.
func TestInputRequiredResult_SatisfiesBothResponseInterfaces(t *testing.T) {
	var _ ToolResponse = InputRequiredResult{}
	var _ PromptResponse = InputRequiredResult{}
}

// TestNewListRootsInputRequest_RoundTrip verifies the helper builds a
// well-formed roots/list InputRequest with an empty params object (no
// fields are defined by the spec) and that the response decoder yields
// the canonical RootsListResult shape.
func TestNewListRootsInputRequest_RoundTrip(t *testing.T) {
	req := NewListRootsInputRequest()
	if req.Method != "roots/list" {
		t.Errorf("Method = %q, want roots/list", req.Method)
	}
	if string(req.Params) != `{}` {
		t.Errorf("Params = %s, want {}", req.Params)
	}

	raw := json.RawMessage(`{"roots":[{"uri":"file:///work/project","name":"project"}]}`)
	got, err := DecodeListRootsInputResponse(raw)
	if err != nil {
		t.Fatalf("DecodeListRootsInputResponse: %v", err)
	}
	if len(got.Roots) != 1 || got.Roots[0].URI != "file:///work/project" {
		t.Errorf("Roots = %+v, want one root with file:///work/project", got.Roots)
	}
}

// TestInputRequiredResultDefaultsResultType verifies that a zero-value
// InputRequiredResult marshals with resultType = "input_required" so
// handlers can build InputRequiredResult{InputRequests: ...} without
// setting the discriminator manually.
func TestInputRequiredResultDefaultsResultType(t *testing.T) {
	data, err := json.Marshal(InputRequiredResult{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["resultType"] != "input_required" {
		t.Errorf("zero-value InputRequiredResult.resultType = %v, want \"input_required\"; got %s",
			m["resultType"], data)
	}
}

// --- SEP-2322 requestState signing ---

// TestRequestState_RoundTrip verifies a freshly signed token verifies cleanly
// and exposes the original taskID. The base case for the HMAC flow.
func TestRequestState_RoundTrip(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	if state == "" {
		t.Fatal("SignRequestState returned empty string")
	}
	got, err := VerifyRequestState(key, state)
	if err != nil {
		t.Fatalf("VerifyRequestState: %v", err)
	}
	if got != "task-abc" {
		t.Errorf("decoded taskID = %q, want %q", got, "task-abc")
	}
}

// TestRequestState_TamperedPayload verifies that any modification to the
// payload bytes (even a single character flip) invalidates the signature.
// This is the core attacker scenario the HMAC defends against.
func TestRequestState_TamperedPayload(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		t.Fatalf("expected '.' separator in signed state %q", state)
	}
	// Re-encode a different payload (taskId swap) but keep the original sig.
	tampered := state[:dot+1] + base64.RawURLEncoding.EncodeToString([]byte(`{"taskId":"task-evil","exp":9999999999}`))
	if _, err := VerifyRequestState(key, tampered); err != ErrRequestStateInvalidSignature {
		t.Errorf("tampered payload: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_TamperedSignature verifies that a flipped signature byte
// (with the original payload intact) is also rejected. Symmetric with the
// payload-tamper case.
func TestRequestState_TamperedSignature(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		t.Fatalf("expected '.' separator")
	}
	// Flip a byte inside the signature (replace first char with one that
	// decodes to different bytes).
	sigChar := byte('A')
	if state[0] == 'A' {
		sigChar = 'B'
	}
	tampered := string(sigChar) + state[1:]
	if _, err := VerifyRequestState(key, tampered); err != ErrRequestStateInvalidSignature {
		t.Errorf("tampered signature: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_Expired verifies that a token whose exp is in the past
// is rejected even when the signature is valid.
func TestRequestState_Expired(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	// Negative TTL = exp in the past.
	state := SignRequestState(key, "task-abc", -time.Second)
	if _, err := VerifyRequestState(key, state); err != ErrRequestStateExpired {
		t.Errorf("expired state: err = %v, want ErrRequestStateExpired", err)
	}
}

// TestRequestState_Malformed batches the structural-failure cases so a
// single test guards every parse path that should map to ErrRequestStateMalformed.
func TestRequestState_Malformed(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	cases := []struct {
		name  string
		state string
	}{
		{"missing separator", "no-dot-here-just-text"},
		{"bad signature base64", "!!!!.eyJ0YXNrSWQiOiJ0YXNrLWFiYyIsImV4cCI6MX0"},
		{"bad payload base64", "abc.!!!!"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyRequestState(key, tc.state); err != ErrRequestStateMalformed {
				t.Errorf("err = %v, want ErrRequestStateMalformed", err)
			}
		})
	}
}

// TestRequestState_WrongKey verifies that a token signed with one key can't
// be verified with a different key — covers the multi-tenant case where
// each tenant has its own signing key.
func TestRequestState_WrongKey(t *testing.T) {
	keyA := []byte("tenant-a-key-32-bytes-padding-here")
	keyB := []byte("tenant-b-key-32-bytes-padding-here")
	state := SignRequestState(keyA, "task-abc", time.Hour)
	if _, err := VerifyRequestState(keyB, state); err != ErrRequestStateInvalidSignature {
		t.Errorf("wrong key: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_BadJSONPayload verifies that a payload that survives
// base64 + HMAC but doesn't parse as the expected JSON shape is reported
// as malformed (not invalid-signature).
func TestRequestState_BadJSONPayload(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	rawPayload := []byte("not-json-at-all")
	mac := hmacFromKey(t, key, rawPayload)
	state := base64.RawURLEncoding.EncodeToString(mac) + "." +
		base64.RawURLEncoding.EncodeToString(rawPayload)
	if _, err := VerifyRequestState(key, state); err != ErrRequestStateMalformed {
		t.Errorf("bad JSON payload: err = %v, want ErrRequestStateMalformed", err)
	}
}

// hmacFromKey is a tiny test helper to compute an HMAC-SHA256 sig over an
// arbitrary payload — used by TestRequestState_BadJSONPayload to forge a
// signature-valid-but-payload-broken token.
func hmacFromKey(t *testing.T, key, payload []byte) []byte {
	t.Helper()
	h := hmac.New(sha256.New, key)
	h.Write(payload)
	return h.Sum(nil)
}
