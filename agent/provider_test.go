package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

func sseServer(t *testing.T, capture *[]byte, lines ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			*capture = body
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
			fl.Flush()
		}
	}))
}

func newTestProvider(t *testing.T, url string) *OpenAIProvider {
	t.Helper()
	p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: url, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func drain(t *testing.T, s Stream) *ProviderResponse {
	t.Helper()
	var acc Accumulator
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		acc.Add(d)
	}
	return acc.Result()
}

func TestOpenAIRequestWireShape(t *testing.T) {
	temp := 0.2
	p := newTestProvider(t, "http://unused")
	body := p.buildBody(ProviderRequest{
		Instructions: "be brief",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"x"}`))}}},
			{Role: RoleTool, ToolCallID: "c1", Text: "echo: x"},
		},
		Tools:       []core.ToolDef{{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}},
		Temperature: &temp,
		MaxTokens:   64,
	}, true)

	got, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"max_tokens":64,"messages":[{"content":"be brief","role":"system"},{"content":"hi","role":"user"},{"role":"assistant","tool_calls":[{"function":{"arguments":"{\"message\":\"x\"}","name":"echo"},"id":"c1","type":"function"}]},{"content":"echo: x","role":"tool","tool_call_id":"c1"}],"model":"test-model","stream":true,"stream_options":{"include_usage":true},"temperature":0.2,"tools":[{"function":{"description":"echoes","name":"echo","parameters":{"type":"object"}},"type":"function"}]}`
	if string(got) != want {
		t.Fatalf("wire shape drift:\n got: %s\nwant: %s", got, want)
	}
}

func TestOpenAIStreamTextFinishUsage(t *testing.T) {
	ts := sseServer(t, nil,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		`[DONE]`,
	)
	defer ts.Close()

	s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)
	if res.Text != "Hello" || res.FinishReason != "stop" {
		t.Fatalf("res = %+v", res)
	}
	if res.Usage == nil || res.Usage.InputTokens != 7 || res.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestOpenAIStreamChunkedParallelToolCalls(t *testing.T) {
	ts := sseServer(t, nil,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a1","function":{"name":"search","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b2","function":{"name":"zoom","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"cats\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	defer ts.Close()

	s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)

	if len(res.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].ID != "a1" || res.ToolCalls[0].Name != "search" || string(res.ToolCalls[0].Args.Raw()) != `{"q":"cats"}` {
		t.Fatalf("call 0 assembly = %+v args=%s", res.ToolCalls[0], res.ToolCalls[0].Args.Raw())
	}
	if res.ToolCalls[1].ID != "b2" || string(res.ToolCalls[1].Args.Raw()) != `{}` {
		t.Fatalf("call 1 = %+v", res.ToolCalls[1])
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
}

func TestOpenAIStreamReasoningDeltas(t *testing.T) {
	ts := sseServer(t, nil,
		`{"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	)
	defer ts.Close()
	s, _ := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	defer s.Close()
	res := drain(t, s)
	if res.Reasoning != "thinking..." || res.Text != "answer" {
		t.Fatalf("res = %+v", res)
	}
}

func TestOpenAIStreamHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"bad key"}`)
	}))
	defer ts.Close()

	_, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	var perr *ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != 401 || !strings.Contains(perr.Body, "bad key") {
		t.Fatalf("want ProviderError 401, got %v", err)
	}
}

func TestOpenAIStreamCancelMidStream(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		fl.Flush()
		<-release
	}))
	defer ts.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	s, err := newTestProvider(t, ts.URL).Stream(ctx, ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if d, err := s.Recv(); err != nil || d.Text != "first" {
		t.Fatalf("first delta: %v %v", d, err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = s.Recv()
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestOpenAIGenerateStructured(t *testing.T) {
	var captured []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"name\":\"trip\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer ts.Close()

	res, err := newTestProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{
		Messages:       []Message{{Role: RoleUser, Text: "name it"}},
		ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"name":"trip"}` || res.Usage.InputTokens != 5 {
		t.Fatalf("res = %+v", res)
	}
	var req map[string]any
	json.Unmarshal(captured, &req)
	rf, ok := req["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("request must carry response_format json_schema, got %v", req["response_format"])
	}
	if _, streaming := req["stream"]; streaming {
		t.Fatal("Generate must not stream")
	}
}

func TestOpenAIGenerateToolCalls(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"t1","function":{"name":"go","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer ts.Close()
	res, err := newTestProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "go" || string(res.ToolCalls[0].Args.Raw()) != `{"x":1}` {
		t.Fatalf("res = %+v", res)
	}
}

func TestStubProviderScriptedThreeStepTurn(t *testing.T) {
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "get_cart", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		StubTurn{ToolCalls: []ToolCall{{ID: "c2", Name: "add_to_cart", Args: core.NewRawJSON(json.RawMessage(`{"item":"milk"}`))}}},
		StubTurn{Text: "Added milk to your cart."},
	)

	var responses []*ProviderResponse
	for i := 0; i < 3; i++ {
		s, err := stub.Stream(context.Background(), ProviderRequest{Messages: []Message{{Role: RoleUser, Text: fmt.Sprintf("round %d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, drain(t, s))
	}

	if responses[0].ToolCalls[0].Name != "get_cart" || responses[0].FinishReason != "tool_calls" {
		t.Fatalf("turn 0 = %+v", responses[0])
	}
	if responses[1].ToolCalls[0].Name != "add_to_cart" {
		t.Fatalf("turn 1 = %+v", responses[1])
	}
	if responses[2].Text != "Added milk to your cart." || responses[2].FinishReason != "stop" {
		t.Fatalf("turn 2 = %+v", responses[2])
	}
	if got := len(stub.Requests()); got != 3 {
		t.Fatalf("recorded requests = %d", got)
	}
	if _, err := stub.Stream(context.Background(), ProviderRequest{}); err == nil {
		t.Fatal("want exhaustion error past the script")
	}
}

func TestStubProviderGenerateAndErrTurn(t *testing.T) {
	boom := errors.New("model down")
	stub := NewStubProvider(
		StubTurn{Text: "fold me"},
		StubTurn{Err: boom},
	)
	res, err := stub.Generate(context.Background(), ProviderRequest{})
	if err != nil || res.Text != "fold me" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := stub.Generate(context.Background(), ProviderRequest{}); !errors.Is(err, boom) {
		t.Fatalf("want scripted error, got %v", err)
	}
}

func TestDeltaKindsJSONRoundTrip(t *testing.T) {
	deltas := []Delta{
		{Kind: DeltaText, Text: "hi"},
		{Kind: DeltaReasoning, Text: "hmm"},
		{Kind: DeltaToolCallStart, Index: 2, ToolCallID: "id", ToolName: "n", Text: "{"},
		{Kind: DeltaToolCallArgs, Index: 2, Text: "}"},
		{Kind: DeltaFinish, FinishReason: "stop"},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: 1, OutputTokens: 2}},
	}
	for _, d := range deltas {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal %s: %v", d.Kind, err)
		}
		var back Delta
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", d.Kind, err)
		}
		raw2, err := json.Marshal(back)
		if err != nil {
			t.Fatalf("re-marshal %s: %v", d.Kind, err)
		}
		if string(raw) != string(raw2) {
			t.Fatalf("round-trip drift for %s: %s vs %s", d.Kind, raw, raw2)
		}
	}
}

func TestAccumulatorEmptyArgsBecomeEmptyObject(t *testing.T) {
	var acc Accumulator
	acc.Add(Delta{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "x", ToolName: "noargs"})
	acc.Add(Delta{Kind: DeltaFinish, FinishReason: "tool_calls"})
	res := acc.Result()
	if string(res.ToolCalls[0].Args.Raw()) != "{}" {
		t.Fatalf("args = %q", res.ToolCalls[0].Args.Raw())
	}
	var m map[string]any
	if err := json.Unmarshal(res.ToolCalls[0].Args.Raw(), &m); err != nil {
		t.Fatalf("args must always unmarshal: %v", err)
	}
}

func TestOpenAIStreamSpecConformantSSE(t *testing.T) {
	// Comment lines, a multi-line data event (spec joins with \n, legal
	// JSON whitespace), and a keepalive blank event must all parse; the
	// pre-servicekit line scanner mishandled the multi-line case.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, ": keepalive comment\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"joined\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer ts.Close()

	s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)
	if res.Text != "joined" || res.FinishReason != "stop" {
		t.Fatalf("res = %+v", res)
	}
}
