package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func newTestAnthropic(t *testing.T, url string) *AnthropicProvider {
	t.Helper()
	p, err := NewAnthropicProvider(AnthropicConfig{BaseURL: url, Model: "claude-test", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// jsonEq compares two JSON documents by structure (key order independent).
func jsonEq(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("unmarshal got: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("wire shape drift:\n got: %s\nwant: %s", got, want)
	}
}

func collectDeltas(t *testing.T, s Stream) []Delta {
	t.Helper()
	var out []Delta
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, d)
	}
	return out
}

func TestAnthropicBuildBodyRolesAndTools(t *testing.T) {
	temp := 0.2
	p := newTestAnthropic(t, "http://unused")
	body := p.buildBody(ProviderRequest{
		Instructions: "be brief",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, Text: "using tool", ToolCalls: []ToolCall{
				{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"x"}`))},
			}},
			{Role: RoleTool, ToolCallID: "c1", Text: "echo: x"},
			{Role: RoleSystem, Text: "state changed"},
		},
		Tools:       []core.ToolDef{{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}},
		Temperature: &temp,
		MaxTokens:   64,
	}, true)

	got, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
		"model":"claude-test",
		"max_tokens":64,
		"system":"be brief",
		"temperature":0.2,
		"stream":true,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"using tool"},
				{"type":"tool_use","id":"c1","name":"echo","input":{"message":"x"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"echo: x"}]},
			{"role":"user","content":[{"type":"text","text":"state changed"}]}
		],
		"tools":[{"name":"echo","description":"echoes","input_schema":{"type":"object"}}]
	}`
	jsonEq(t, got, want)
}

func TestAnthropicBuildBodyDefaultMaxTokensNoTools(t *testing.T) {
	p := newTestAnthropic(t, "http://unused")
	// No per-request MaxTokens, no tools, no instructions → config default
	// max_tokens, no tools/tool_choice/system keys.
	body := p.buildBody(ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, false)
	if body["max_tokens"] != defaultAnthropicMaxTokens {
		t.Fatalf("max_tokens = %v, want %d", body["max_tokens"], defaultAnthropicMaxTokens)
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("tools must be absent when none supplied")
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatal("tool_choice must be absent when no tools")
	}
	if _, ok := body["system"]; ok {
		t.Fatal("system must be absent when no instructions")
	}
	if _, ok := body["stream"]; ok {
		t.Fatal("stream must be absent for Generate")
	}
}

func TestAnthropicToolChoiceMapping(t *testing.T) {
	p := newTestAnthropic(t, "http://unused")
	tools := []core.ToolDef{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}
	cases := []struct {
		choice ToolChoice
		want   any // nil means the tool_choice key must be absent
	}{
		{ToolChoice{}, nil},
		{ToolChoiceAuto, map[string]any{"type": "auto"}},
		{ToolChoiceRequired, map[string]any{"type": "any"}},
		{ToolChoiceNone, map[string]any{"type": "none"}},
		{ToolChoiceFunc("echo"), map[string]any{"type": "tool", "name": "echo"}},
	}
	for _, c := range cases {
		body := p.buildBody(ProviderRequest{
			Messages:   []Message{{Role: RoleUser, Text: "hi"}},
			Tools:      tools,
			ToolChoice: c.choice,
		}, true)
		got, present := body["tool_choice"]
		if c.want == nil {
			if present {
				t.Fatalf("choice %+v: tool_choice must be absent, got %v", c.choice, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("choice %+v: tool_choice = %v, want %v", c.choice, got, c.want)
		}
	}
}

func TestAnthropicStreamTextFinishUsage(t *testing.T) {
	ts := sseServer(t, nil,
		`{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	)
	defer ts.Close()

	s, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := collectDeltas(t, s)
	want := []Delta{
		{Kind: DeltaText, Text: "Hel"},
		{Kind: DeltaText, Text: "lo"},
		{Kind: DeltaFinish, FinishReason: "end_turn"},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: 12, OutputTokens: 9}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delta sequence:\n got: %+v\nwant: %+v", got, want)
	}

	// The Accumulator folds the same stream into the completed response.
	var acc Accumulator
	for _, d := range got {
		acc.Add(d)
	}
	res := acc.Result()
	if res.Text != "Hello" || res.FinishReason != "end_turn" {
		t.Fatalf("folded res = %+v", res)
	}
	if res.Usage == nil || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestAnthropicToolNameSanitizedAndReversed(t *testing.T) {
	var reqBody []byte
	ts := sseServer(t, &reqBody,
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"weather_current"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	)
	defer ts.Close()

	s, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{
		Tools: []core.ToolDef{{Name: "weather.current", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var acc Accumulator
	for _, d := range collectDeltas(t, s) {
		acc.Add(d)
	}
	res := acc.Result()

	if !strings.Contains(string(reqBody), `"name":"weather_current"`) || strings.Contains(string(reqBody), "weather.current") {
		t.Fatalf("tools-list name not sanitized in the request:\n%s", reqBody)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "weather.current" {
		t.Fatalf("tool call name not reversed on the response: %+v", res.ToolCalls)
	}
}

func TestAnthropicStreamToolUse(t *testing.T) {
	ts := sseServer(t, nil,
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"search"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"cats\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	)
	defer ts.Close()

	s, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := collectDeltas(t, s)
	want := []Delta{
		{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "toolu_1", ToolName: "search"},
		{Kind: DeltaToolCallArgs, Index: 0, Text: `{"q":`},
		{Kind: DeltaToolCallArgs, Index: 0, Text: `"cats"}`},
		{Kind: DeltaFinish, FinishReason: "tool_use"},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: 5, OutputTokens: 7}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delta sequence:\n got: %+v\nwant: %+v", got, want)
	}

	var acc Accumulator
	for _, d := range got {
		acc.Add(d)
	}
	res := acc.Result()
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	call := res.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "search" || string(call.Args.Raw()) != `{"q":"cats"}` {
		t.Fatalf("folded call = %+v args=%s", call, call.Args.Raw())
	}
	if res.FinishReason != "tool_use" {
		t.Fatalf("finish = %q", res.FinishReason)
	}
}

func TestAnthropicStreamThinking(t *testing.T) {
	ts := sseServer(t, nil,
		`{"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	)
	defer ts.Close()

	s, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res := drain(t, s)
	if res.Reasoning != "pondering" || res.Text != "answer" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAnthropicStreamHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer ts.Close()

	_, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	var perr *ProviderError
	if !(err != nil) {
		t.Fatal("want error")
	}
	if pe, ok := err.(*ProviderError); ok {
		perr = pe
	}
	if perr == nil || perr.StatusCode != 401 {
		t.Fatalf("want ProviderError 401, got %v", err)
	}
}

func TestAnthropicHeaders(t *testing.T) {
	var gotKey, gotVersion, gotType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotType = r.Header.Get("Content-Type")
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer ts.Close()

	if _, err := newTestAnthropic(t, ts.URL).Generate(context.Background(), ProviderRequest{}); err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-test" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if gotVersion != DefaultAnthropicVersion {
		t.Fatalf("anthropic-version = %q", gotVersion)
	}
	if gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
}

func TestAnthropicGenerate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"text","text":"Hi there"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":4}}`)
	}))
	defer ts.Close()

	res, err := newTestAnthropic(t, ts.URL).Generate(context.Background(), ProviderRequest{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hi there" || res.FinishReason != "end_turn" {
		t.Fatalf("res = %+v", res)
	}
	if res.Usage == nil || res.Usage.InputTokens != 8 || res.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestAnthropicGenerateToolCalls(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"text","text":"let me"},{"type":"tool_use","id":"toolu_9","name":"go","input":{"x":1}}],"stop_reason":"tool_use"}`)
	}))
	defer ts.Close()

	res, err := newTestAnthropic(t, ts.URL).Generate(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "let me" || res.FinishReason != "tool_use" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "go" || string(res.ToolCalls[0].Args.Raw()) != `{"x":1}` {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
}

func TestAnthropicGenerateStructured(t *testing.T) {
	var captured []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"content":[{"type":"tool_use","id":"t","name":"structured_output","input":{"name":"trip"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
	defer ts.Close()

	res, err := newTestAnthropic(t, ts.URL).Generate(context.Background(), ProviderRequest{
		Messages:       []Message{{Role: RoleUser, Text: "name it"}},
		ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Structured output surfaces in Text, not as a tool call.
	if res.Text != `{"name":"trip"}` {
		t.Fatalf("structured text = %q", res.Text)
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("synthetic tool must not surface as a tool call: %+v", res.ToolCalls)
	}
	if res.Usage == nil || res.Usage.InputTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}

	// The request forces the synthetic tool via tool_choice.
	var req map[string]any
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatal(err)
	}
	tc, ok := req["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "tool" || tc["name"] != structuredOutputToolName {
		t.Fatalf("tool_choice must force the synthetic tool, got %v", req["tool_choice"])
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", req["tools"])
	}
	synth := tools[0].(map[string]any)
	if synth["name"] != structuredOutputToolName {
		t.Fatalf("synthetic tool = %v", synth)
	}
	if _, streaming := req["stream"]; streaming {
		t.Fatal("Generate must not stream")
	}
}
