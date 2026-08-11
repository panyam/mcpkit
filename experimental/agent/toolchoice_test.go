package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestToolChoiceWireForms(t *testing.T) {
	p := &OpenAIProvider{cfg: OpenAIConfig{BaseURL: "http://x", Model: "m"}}
	tools := []core.ToolDef{{Name: "t", Description: "d", InputSchema: map[string]any{"type": "object"}}}

	cases := []struct {
		choice ToolChoice
		want   string // JSON of the tool_choice value, or "" if absent
	}{
		{ToolChoice{}, ""},
		{ToolChoiceAuto, `"auto"`},
		{ToolChoiceRequired, `"required"`},
		{ToolChoiceNone, `"none"`},
		{ToolChoiceFunc("send_email"), `{"function":{"name":"send_email"},"type":"function"}`},
	}
	for _, tc := range cases {
		body := p.buildBody(ProviderRequest{Tools: tools, ToolChoice: tc.choice}, false)
		got, present := body["tool_choice"]
		if tc.want == "" {
			if present {
				t.Fatalf("choice %+v must emit no tool_choice, got %v", tc.choice, got)
			}
			continue
		}
		raw, _ := json.Marshal(got)
		if string(raw) != tc.want {
			t.Fatalf("choice %+v wire = %s, want %s", tc.choice, raw, tc.want)
		}
	}
}

func TestToolChoiceOmittedWithoutTools(t *testing.T) {
	p := &OpenAIProvider{cfg: OpenAIConfig{BaseURL: "http://x", Model: "m"}}
	body := p.buildBody(ProviderRequest{ToolChoice: ToolChoiceRequired}, false)
	if _, present := body["tool_choice"]; present {
		t.Fatal("tool_choice must be omitted when no tools are offered")
	}
}

func TestToolChoiceReachesTheWire(t *testing.T) {
	var captured map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer ts.Close()
	p, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: ts.URL, Model: "m"})
	s, err := p.Stream(context.Background(), ProviderRequest{
		Tools:      []core.ToolDef{{Name: "x", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: ToolChoiceFunc("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	tc, ok := captured["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "function" {
		t.Fatalf("streamed tool_choice = %v", captured["tool_choice"])
	}
}
