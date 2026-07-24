package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// DefaultAnthropicVersion is the anthropic-version header sent when
// AnthropicConfig.Version is empty. 2023-06-01 is the documented stable
// Messages API version.
const DefaultAnthropicVersion = "2023-06-01"

// defaultAnthropicMaxTokens is the max_tokens sent when AnthropicConfig.MaxTokens
// is not positive. The Anthropic Messages API requires max_tokens on every
// request.
const defaultAnthropicMaxTokens = 4096

// structuredOutputToolName is the synthetic tool used to coerce structured
// output on Generate: Anthropic has no response_format, so a forced tool whose
// input_schema is the caller's ResponseSchema is the equivalent mechanism.
const structuredOutputToolName = "structured_output"

// AnthropicConfig configures the Anthropic Messages API endpoint.
type AnthropicConfig struct {
	// BaseURL is the API root. The provider appends "/v1/messages". Defaults
	// to "https://api.anthropic.com" when empty.
	BaseURL string

	// APIKey is sent as the x-api-key header. Required against the real API;
	// may be empty for a local mock.
	APIKey string

	// Model is the model identifier sent on every request (required). The
	// caller supplies it; the provider hardcodes none.
	Model string

	// MaxTokens caps the completion length sent on every request. Defaults to
	// 4096 when not positive; a per-request ProviderRequest.MaxTokens overrides
	// it.
	MaxTokens int

	// Version is the anthropic-version header. Defaults to
	// DefaultAnthropicVersion when empty.
	Version string

	// HTTPClient overrides http.DefaultClient. Set this for proxies, custom
	// TLS, or timeouts (note: an overall client timeout also bounds streaming
	// reads; prefer per-request ctx deadlines for streams).
	HTTPClient *http.Client
}

// AnthropicProvider implements Provider over the Anthropic Messages API wire
// with no SDK dependency (net/http plus servicekit's WHATWG-conformant SSE
// reader). Safe for concurrent use.
type AnthropicProvider struct {
	cfg  AnthropicConfig
	http *http.Client
}

// NewAnthropicProvider validates cfg and returns a provider. Model is required;
// BaseURL, MaxTokens, and Version fall back to defaults.
func NewAnthropicProvider(cfg AnthropicConfig) (*AnthropicProvider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("agent: AnthropicConfig requires Model")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	if cfg.Version == "" {
		cfg.Version = DefaultAnthropicVersion
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &AnthropicProvider{cfg: cfg, http: hc}, nil
}

// Stream implements Provider. Anthropic SSE events map onto the Delta taxonomy:
// content_block_start(tool_use) → DeltaToolCallStart, text_delta → DeltaText,
// input_json_delta → DeltaToolCallArgs, thinking_delta → DeltaReasoning,
// message_delta → DeltaFinish + DeltaUsage. The stream ends with io.EOF after
// message_stop.
func (p *AnthropicProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	body := p.buildBody(req, true)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	_, safeToReal := toolNameMaps(req.Tools)
	st := &anthropicStream{nameMap: safeToReal}
	st.sseStream = sseStream{ctx: ctx, body: resp.Body, events: ssehttp.NewSSEEventReader(resp.Body), decode: st.decode}
	return st, nil
}

// Generate implements Provider with a non-streaming request. When
// req.ResponseSchema is set, the request forces a synthetic tool whose
// input_schema is the schema and the tool_use input is returned in
// ProviderResponse.Text (Anthropic has no response_format).
func (p *AnthropicProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	structured := req.ResponseSchema.Len() > 0
	body := p.buildBody(req, false)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var full struct {
		Content    []anthropicContentBlock `json:"content"`
		StopReason string                  `json:"stop_reason"`
		Usage      *anthropicUsage         `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, fmt.Errorf("agent: decode message: %w", err)
	}

	out := &ProviderResponse{FinishReason: full.StopReason}
	_, safeToReal := toolNameMaps(req.Tools)
	var text, reason strings.Builder
	for _, block := range full.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			reason.WriteString(block.Thinking)
		case "tool_use":
			args := block.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			if structured && block.Name == structuredOutputToolName {
				// Structured output surfaces in Text (mirrors the OpenAI
				// response_format path), not as a tool call.
				out.Text = string(args)
				continue
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: realToolName(safeToReal, block.Name), Args: core.NewRawJSON(args)})
		}
	}
	if out.Text == "" {
		out.Text = text.String()
	}
	out.Reasoning = reason.String()
	if full.Usage != nil {
		out.Usage = &Usage{InputTokens: full.Usage.InputTokens, OutputTokens: full.Usage.OutputTokens}
	}
	return out, nil
}

func (p *AnthropicProvider) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(p.cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", p.cfg.Version)
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.cfg.APIKey)
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

// buildBody produces the Messages API request. Kept as one method so the
// wire-shape test pins the exact JSON we emit.
func (p *AnthropicProvider) buildBody(req ProviderRequest, stream bool) map[string]any {
	realToSafe, _ := toolNameMaps(req.Tools)
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": m.Text}},
			})
		case RoleAssistant:
			content := make([]map[string]any, 0, len(m.ToolCalls)+1)
			if m.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Text})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  safeToolName(realToSafe, tc.Name),
					"input": rawToInput(tc.Args),
				})
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
		case RoleTool:
			msgs = append(msgs, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     m.Text,
				}},
			})
		default:
			// RoleSystem carries mcpkit injected-context messages that live
			// mid-history. Anthropic has no inline system role in the messages
			// array (system is a top-level string), so each such message maps
			// to a plain user text turn.
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": m.Text}},
			})
		}
	}

	maxTokens := p.cfg.MaxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	body := map[string]any{
		"model":      p.cfg.Model,
		"messages":   msgs,
		"max_tokens": maxTokens,
	}
	if req.Instructions != "" {
		body["system"] = req.Instructions
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	tools := make([]map[string]any, 0, len(req.Tools)+1)
	for _, td := range req.Tools {
		tools = append(tools, map[string]any{
			"name":         realToSafe[td.Name],
			"description":  td.Description,
			"input_schema": td.InputSchema,
		})
	}

	structured := !stream && req.ResponseSchema.Len() > 0
	if structured {
		var schema any
		if req.ResponseSchema.Bind(&schema) == nil {
			tools = append(tools, map[string]any{
				"name":         structuredOutputToolName,
				"description":  "Return the response as structured output conforming to the schema.",
				"input_schema": schema,
			})
			body["tools"] = tools
			body["tool_choice"] = map[string]any{"type": "tool", "name": structuredOutputToolName}
		}
	} else if len(tools) > 0 {
		body["tools"] = tools
		choice := req.ToolChoice
		if choice.Mode == "function" {
			choice.Name = safeToolName(realToSafe, choice.Name) // match the sanitized tools-list name
		}
		if tc := anthropicToolChoice(choice); tc != nil {
			body["tool_choice"] = tc
		}
	}

	if stream {
		body["stream"] = true
	}
	return body
}

// rawToInput decodes a tool call's JSON arguments into the object Anthropic's
// tool_use.input expects. An empty or unparseable value becomes an empty
// object.
func rawToInput(args core.RawJSON) any {
	if args.Len() == 0 {
		return map[string]any{}
	}
	var v any
	if args.Bind(&v) != nil || v == nil {
		return map[string]any{}
	}
	return v
}

// anthropicToolChoice renders the Anthropic tool_choice value, or nil when the
// choice is the provider default (auto is implicit, so we omit it).
func anthropicToolChoice(tc ToolChoice) any {
	switch tc.Mode {
	case "auto":
		return map[string]any{"type": "auto"}
	case "required":
		return map[string]any{"type": "any"}
	case "none":
		return map[string]any{"type": "none"}
	case "function":
		return map[string]any{"type": "tool", "name": tc.Name}
	default: // "" — provider default
		return nil
	}
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// anthropicSSE is one decoded SSE data payload. Anthropic events are dispatched
// on the JSON "type" field (which duplicates the SSE "event:" name), so the
// stream adapter never needs the event line.
type anthropicSSE struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicStream adapts the SSE body to the Stream interface via servicekit's
// WHATWG-conformant SSEEventReader. Recv keeps a small queue because one SSE
// event can expand to several Deltas (message_delta carries both finish and
// usage). inputTokens is captured at message_start and folded into the usage
// delta emitted at message_delta.
type anthropicStream struct {
	sseStream
	inputTokens int
	nameMap     map[string]string // safe→real tool name, to reverse sanitized names on tool_use blocks
}

// decode unmarshals one Anthropic SSE event and expands it into Deltas.
func (s *anthropicStream) decode(payload string) ([]Delta, bool, error) {
	var msg anthropicSSE
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return nil, false, fmt.Errorf("agent: bad SSE event: %w", err)
	}
	return s.eventDeltas(msg)
}

// eventDeltas maps one typed Anthropic event to Deltas, tracking input tokens
// across the message so the final usage delta carries both counts. "message_stop"
// ends the stream; an "error" event surfaces as a decode error.
func (s *anthropicStream) eventDeltas(msg anthropicSSE) ([]Delta, bool, error) {
	switch msg.Type {
	case "message_start":
		if msg.Message != nil && msg.Message.Usage != nil {
			s.inputTokens = msg.Message.Usage.InputTokens
		}
	case "content_block_start":
		if msg.ContentBlock != nil && msg.ContentBlock.Type == "tool_use" {
			return []Delta{{
				Kind:       DeltaToolCallStart,
				Index:      msg.Index,
				ToolCallID: msg.ContentBlock.ID,
				ToolName:   realToolName(s.nameMap, msg.ContentBlock.Name),
			}}, false, nil
		}
	case "content_block_delta":
		if msg.Delta == nil {
			return nil, false, nil
		}
		switch msg.Delta.Type {
		case "text_delta":
			if msg.Delta.Text != "" {
				return []Delta{{Kind: DeltaText, Text: msg.Delta.Text}}, false, nil
			}
		case "input_json_delta":
			if msg.Delta.PartialJSON != "" {
				return []Delta{{Kind: DeltaToolCallArgs, Index: msg.Index, Text: msg.Delta.PartialJSON}}, false, nil
			}
		case "thinking_delta":
			if msg.Delta.Thinking != "" {
				return []Delta{{Kind: DeltaReasoning, Text: msg.Delta.Thinking}}, false, nil
			}
		}
	case "message_delta":
		var out []Delta
		if msg.Delta != nil && msg.Delta.StopReason != "" {
			out = append(out, Delta{Kind: DeltaFinish, FinishReason: msg.Delta.StopReason})
		}
		if msg.Usage != nil {
			out = append(out, Delta{Kind: DeltaUsage, Usage: &Usage{
				InputTokens:  s.inputTokens,
				OutputTokens: msg.Usage.OutputTokens,
			}})
		}
		return out, false, nil
	case "message_stop":
		return nil, true, nil
	case "error":
		if msg.Error != nil {
			return nil, false, fmt.Errorf("agent: anthropic stream error (%s): %s", msg.Error.Type, msg.Error.Message)
		}
		return nil, false, errors.New("agent: anthropic stream error")
	}
	// content_block_stop and ping carry no deltas.
	return nil, false, nil
}
