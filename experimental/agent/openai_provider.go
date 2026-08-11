package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// OpenAIConfig configures an OpenAI-compatible chat-completions endpoint
// (OpenAI, lmstudio, vllm, LiteLLM-style proxies, gateways).
type OpenAIConfig struct {
	// BaseURL is the API root including any version prefix, e.g.
	// "http://localhost:1234/v1". The provider appends "/chat/completions".
	BaseURL string

	// APIKey, when non-empty, is sent as a Bearer token. Local servers
	// commonly need none.
	APIKey string

	// Model is the model identifier sent on every request.
	Model string

	// HTTPClient overrides http.DefaultClient. Set this for proxies,
	// custom TLS, or timeouts (note: an overall client timeout also bounds
	// streaming reads; prefer per-request ctx deadlines for streams).
	HTTPClient *http.Client
}

// OpenAIProvider implements Provider over the OpenAI-compatible
// chat-completions wire with no SDK dependency (net/http plus servicekit's
// WHATWG-conformant SSE reader). Safe for concurrent use.
type OpenAIProvider struct {
	cfg  OpenAIConfig
	http *http.Client
}

// NewOpenAIProvider validates cfg and returns a provider. BaseURL and Model
// are required.
func NewOpenAIProvider(cfg OpenAIConfig) (*OpenAIProvider, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, fmt.Errorf("agent: OpenAIConfig requires BaseURL and Model")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &OpenAIProvider{cfg: cfg, http: hc}, nil
}

// ProviderError reports a non-2xx response from the model endpoint. The body
// is included verbatim (truncated to 2 KB) because OpenAI-compatible servers
// put the useful diagnostics there.
type ProviderError struct {
	StatusCode int
	Body       string
}

// Error implements error.
func (e *ProviderError) Error() string {
	return fmt.Sprintf("agent: provider returned HTTP %d: %s", e.StatusCode, e.Body)
}

// Stream implements Provider. Deltas map 1:1 from SSE chunks; tool calls
// arrive as DeltaToolCallStart followed by DeltaToolCallArgs fragments on the
// same index. The stream ends with io.EOF after the servers [DONE] marker.
func (p *OpenAIProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	body := p.buildBody(req, true)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	_, safeToReal := toolNameMaps(req.Tools)
	st := &openaiStream{nameMap: safeToReal}
	st.sseStream = sseStream{ctx: ctx, body: resp.Body, events: ssehttp.NewSSEEventReader(resp.Body), decode: st.decode}
	return st, nil
}

// Generate implements Provider with a non-streaming request. When
// req.ResponseSchema is set, the request carries response_format json_schema
// and the structured document is returned in ProviderResponse.Text.
func (p *OpenAIProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	body := p.buildBody(req, false)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var full struct {
		Choices []struct {
			Message struct {
				Content          string       `json:"content"`
				ReasoningContent string       `json:"reasoning_content"`
				ToolCalls        []oaToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *oaUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, fmt.Errorf("agent: decode completion: %w", err)
	}
	if len(full.Choices) == 0 {
		return nil, fmt.Errorf("agent: completion had no choices")
	}
	choice := full.Choices[0]
	out := &ProviderResponse{
		Text:         choice.Message.Content,
		Reasoning:    choice.Message.ReasoningContent,
		FinishReason: choice.FinishReason,
	}
	_, safeToReal := toolNameMaps(req.Tools)
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: realToolName(safeToReal, tc.Function.Name), Args: core.NewRawJSON(json.RawMessage(args))})
	}
	if full.Usage != nil {
		out.Usage = &Usage{InputTokens: full.Usage.PromptTokens, OutputTokens: full.Usage.CompletionTokens}
	}
	return out, nil
}

func (p *OpenAIProvider) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(p.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &ProviderError{StatusCode: resp.StatusCode, Body: string(msg)}
	}
	return resp, nil
}

// buildBody produces the chat-completions request. Kept as one method so the
// wire-shape test pins the exact JSON we emit.
func (p *OpenAIProvider) buildBody(req ProviderRequest, stream bool) map[string]any {
	realToSafe, _ := toolNameMaps(req.Tools)
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.Instructions})
	}
	for _, m := range req.Messages {
		entry := map[string]any{"role": string(m.Role)}
		switch m.Role {
		case RoleTool:
			entry["tool_call_id"] = m.ToolCallID
			entry["content"] = m.Text
		case RoleAssistant:
			if m.Text != "" || len(m.ToolCalls) == 0 {
				entry["content"] = m.Text
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					calls[i] = map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      safeToolName(realToSafe, tc.Name),
							"arguments": string(tc.Args.Raw()),
						},
					}
				}
				entry["tool_calls"] = calls
			}
		default:
			entry["content"] = m.Text
		}
		msgs = append(msgs, entry)
	}

	body := map[string]any{
		"model":    p.cfg.Model,
		"messages": msgs,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, td := range req.Tools {
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        realToSafe[td.Name],
					"description": td.Description,
					"parameters":  td.InputSchema,
				},
			}
		}
		body["tools"] = tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 && !req.ToolChoice.IsZero() {
		choice := req.ToolChoice
		if choice.Mode == "function" {
			choice.Name = safeToolName(realToSafe, choice.Name) // match the sanitized tools-list name
		}
		if tc := choice.wire(); tc != nil {
			body["tool_choice"] = tc
		}
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	} else if req.ResponseSchema.Len() > 0 {
		var schema any
		if req.ResponseSchema.Bind(&schema) == nil {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "response",
					"schema": schema,
				},
			}
		}
	}
	return body
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			Reasoning        string       `json:"reasoning"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
}

// openaiStream adapts the SSE body to the Stream interface via servicekit's
// WHATWG-conformant SSEEventReader (multi-line data joining, BOM stripping,
// comment skipping). Recv keeps a small queue because one SSE event can
// expand to several Deltas.
type openaiStream struct {
	sseStream
	started map[int]bool
	nameMap map[string]string // safe→real tool name, to reverse sanitized names on tool_call deltas
}

// decode turns one SSE data payload into Deltas. The "[DONE]" sentinel ends the
// stream; otherwise the chunk is unmarshalled and expanded via chunkDeltas.
func (s *openaiStream) decode(payload string) ([]Delta, bool, error) {
	if payload == "[DONE]" {
		return nil, true, nil
	}
	var chunk oaChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil, false, fmt.Errorf("agent: bad SSE chunk: %w", err)
	}
	return s.chunkDeltas(chunk), false, nil
}

// chunkDeltas expands one chat-completions chunk into Deltas. It keeps the
// per-index started map so a tool call's first fragment is a DeltaToolCallStart
// and the rest are DeltaToolCallArgs.
func (s *openaiStream) chunkDeltas(chunk oaChunk) []Delta {
	var out []Delta
	if chunk.Usage != nil {
		out = append(out, Delta{Kind: DeltaUsage, Usage: &Usage{
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
		}})
	}
	if len(chunk.Choices) == 0 {
		return out
	}
	choice := chunk.Choices[0]
	if choice.Delta.Content != "" {
		out = append(out, Delta{Kind: DeltaText, Text: choice.Delta.Content})
	}
	if r := choice.Delta.ReasoningContent + choice.Delta.Reasoning; r != "" {
		out = append(out, Delta{Kind: DeltaReasoning, Text: r})
	}
	for _, tc := range choice.Delta.ToolCalls {
		if s.started == nil {
			s.started = make(map[int]bool)
		}
		if !s.started[tc.Index] {
			s.started[tc.Index] = true
			out = append(out, Delta{
				Kind:       DeltaToolCallStart,
				Index:      tc.Index,
				ToolCallID: tc.ID,
				ToolName:   realToolName(s.nameMap, tc.Function.Name),
				Text:       tc.Function.Arguments,
			})
			continue
		}
		if tc.Function.Arguments != "" {
			out = append(out, Delta{Kind: DeltaToolCallArgs, Index: tc.Index, Text: tc.Function.Arguments})
		}
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		out = append(out, Delta{Kind: DeltaFinish, FinishReason: *choice.FinishReason})
	}
	return out
}
