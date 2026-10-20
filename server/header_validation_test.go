package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func makeReq(t *testing.T, method string, params any) *core.Request {
	t.Helper()
	r := &core.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		r.Params = core.NewRawJSON(raw)
	}
	return r
}

func TestValidateRoutingHeaders_MismatchedMethod(t *testing.T) {
	req := makeReq(t, "tools/list", nil)
	h := http.Header{}
	h.Set("Mcp-Method", "prompts/list")
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_MissingMethod(t *testing.T) {
	req := makeReq(t, "tools/list", nil)
	h := http.Header{}
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_MismatchedName(t *testing.T) {
	req := makeReq(t, "tools/call", map[string]any{"name": "greet", "arguments": map[string]any{}})
	h := http.Header{}
	h.Set("Mcp-Method", "tools/call")
	h.Set("Mcp-Name", "wrong_tool")
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch for name mismatch, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_MissingNameWhenBodyHasName(t *testing.T) {
	req := makeReq(t, "tools/call", map[string]any{"name": "greet", "arguments": map[string]any{}})
	h := http.Header{}
	h.Set("Mcp-Method", "tools/call")
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch for missing Mcp-Name, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_CaseSensitiveValue(t *testing.T) {
	req := makeReq(t, "tools/list", nil)
	h := http.Header{}
	h.Set("Mcp-Method", "TOOLS/LIST")
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch for case-mismatched header value, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_OWSStrippedFromName(t *testing.T) {
	req := makeReq(t, "tools/call", map[string]any{"name": "greet", "arguments": map[string]any{}})
	h := http.Header{}
	h.Set("Mcp-Method", "tools/call")
	h.Set("Mcp-Name", "  greet  ")
	if resp := validateRoutingHeaders(req, h); resp != nil {
		t.Fatalf("OWS-trimmed Mcp-Name should validate, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_HeaderNameCaseInsensitive(t *testing.T) {
	req := makeReq(t, "tools/list", nil)
	for _, name := range []string{"mcp-method", "MCP-METHOD", "Mcp-Method", "mCp-MeThOd"} {
		h := http.Header{}
		h.Set(name, "tools/list")
		if resp := validateRoutingHeaders(req, h); resp != nil {
			t.Errorf("header name %q should match case-insensitively, got %+v", name, resp)
		}
	}
}

func TestValidateRoutingHeaders_MatchingMethodOnly(t *testing.T) {
	req := makeReq(t, "tools/list", nil)
	h := http.Header{}
	h.Set("Mcp-Method", "tools/list")
	if resp := validateRoutingHeaders(req, h); resp != nil {
		t.Fatalf("matching Mcp-Method on no-name method should pass, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_MatchingResourcesReadURI(t *testing.T) {
	req := makeReq(t, "resources/read", map[string]any{"uri": "file:///tmp/foo.txt"})
	h := http.Header{}
	h.Set("Mcp-Method", "resources/read")
	h.Set("Mcp-Name", "file:///tmp/foo.txt")
	if resp := validateRoutingHeaders(req, h); resp != nil {
		t.Fatalf("matching Mcp-Name=uri on resources/read should pass, got %+v", resp)
	}
}

func TestValidateRoutingHeaders_MismatchedResourcesReadURI(t *testing.T) {
	req := makeReq(t, "resources/read", map[string]any{"uri": "file:///tmp/foo.txt"})
	h := http.Header{}
	h.Set("Mcp-Method", "resources/read")
	h.Set("Mcp-Name", "file:///tmp/other.txt")
	resp := validateRoutingHeaders(req, h)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
		t.Fatalf("expected -32020 HeaderMismatch for URI mismatch, got %+v", resp)
	}
}

// SEP-2243 lists prompts/get among the name-carrying methods
// (params.name). mcpkit's own client stamps Mcp-Name for prompts/get via
// core.DeriveMcpName, so the server must validate it symmetrically with
// tools/call — matched passes, mismatched and missing are rejected.
func TestValidateRoutingHeaders_PromptsGetCarriesName(t *testing.T) {
	t.Run("matched", func(t *testing.T) {
		req := makeReq(t, "prompts/get", map[string]any{"name": "greeting", "arguments": map[string]any{}})
		h := http.Header{}
		h.Set("Mcp-Method", "prompts/get")
		h.Set("Mcp-Name", "greeting")
		if resp := validateRoutingHeaders(req, h); resp != nil {
			t.Fatalf("matched Mcp-Name on prompts/get should pass, got %+v", resp)
		}
	})
	t.Run("mismatched", func(t *testing.T) {
		req := makeReq(t, "prompts/get", map[string]any{"name": "greeting"})
		h := http.Header{}
		h.Set("Mcp-Method", "prompts/get")
		h.Set("Mcp-Name", "wrong_prompt")
		resp := validateRoutingHeaders(req, h)
		if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
			t.Fatalf("expected -32020 HeaderMismatch for prompts/get name mismatch, got %+v", resp)
		}
	})
	t.Run("missing", func(t *testing.T) {
		req := makeReq(t, "prompts/get", map[string]any{"name": "greeting"})
		h := http.Header{}
		h.Set("Mcp-Method", "prompts/get")
		resp := validateRoutingHeaders(req, h)
		if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
			t.Fatalf("expected -32020 HeaderMismatch for missing Mcp-Name on prompts/get, got %+v", resp)
		}
	})
}

// SEP-2663 elevates Mcp-Name: <taskId> to a required client header on
// tasks/get, tasks/update, and tasks/cancel. The SEP-2243 universal
// MUST therefore applies — server rejects mismatched or missing
// Mcp-Name with -32020 HeaderMismatch.
func TestValidateRoutingHeaders_TasksMethodsCarryTaskID(t *testing.T) {
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		method := method
		t.Run(method+"/matched", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"taskId": "task-abc"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			h.Set("Mcp-Name", "task-abc")
			if resp := validateRoutingHeaders(req, h); resp != nil {
				t.Fatalf("matched Mcp-Name should pass, got %+v", resp)
			}
		})
		t.Run(method+"/mismatched", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"taskId": "task-abc"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			h.Set("Mcp-Name", "task-xyz")
			resp := validateRoutingHeaders(req, h)
			if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
				t.Fatalf("expected -32020 HeaderMismatch for taskId mismatch, got %+v", resp)
			}
		})
		t.Run(method+"/missing", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"taskId": "task-abc"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			resp := validateRoutingHeaders(req, h)
			if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
				t.Fatalf("expected -32020 HeaderMismatch for missing Mcp-Name, got %+v", resp)
			}
		})
	}
}

// SEP-3415 § Streamable HTTP: Routing Headers puts Mcp-Name: <params.name>
// on the four name-carrying events methods. events/list carries none.
func TestValidateRoutingHeaders_EventsMethodsCarryName(t *testing.T) {
	for _, method := range []string{"events/poll", "events/stream", "events/subscribe", "events/unsubscribe"} {
		t.Run(method+"/matched", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"name": "tick"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			h.Set("Mcp-Name", "tick")
			if resp := validateRoutingHeaders(req, h); resp != nil {
				t.Fatalf("matched Mcp-Name should pass, got %+v", resp)
			}
		})
		t.Run(method+"/mismatched", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"name": "tick"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			h.Set("Mcp-Name", "tock")
			resp := validateRoutingHeaders(req, h)
			if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
				t.Fatalf("expected -32020 for mismatched Mcp-Name, got %+v", resp)
			}
		})
		t.Run(method+"/missing", func(t *testing.T) {
			req := makeReq(t, method, map[string]any{"name": "tick"})
			h := http.Header{}
			h.Set("Mcp-Method", method)
			resp := validateRoutingHeaders(req, h)
			if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
				t.Fatalf("expected -32020 for missing Mcp-Name, got %+v", resp)
			}
		})
	}
	t.Run("events/list/no-name", func(t *testing.T) {
		req := makeReq(t, "events/list", map[string]any{})
		h := http.Header{}
		h.Set("Mcp-Method", "events/list")
		if resp := validateRoutingHeaders(req, h); resp != nil {
			t.Fatalf("events/list without Mcp-Name should pass, got %+v", resp)
		}
	})
}

// SEP-2243 § Server Validation: servers MUST decode a Base64-sentinel
// Mcp-Name before comparing, and an undecodable one is a validation failure.
func TestValidateRoutingHeaders_Base64SentinelName(t *testing.T) {
	for _, tc := range []struct {
		name, method, bodyName, header string
		wantErr                        bool
	}{
		{"tool-non-ascii", "tools/call", "Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?=", false},
		{"event-non-ascii", "events/poll", "Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?=", false},
		{"encoded-wrong-value", "tools/call", "greet", "=?base64?SGVsbG8sIOS4lueVjA==?=", true},
		{"raw-sentinel-matches-body-literally", "tools/call", "=?base64?SGk=?=", "=?base64?SGk=?=", true},
		{"malformed", "tools/call", "greet", "=?base64?!!!?=", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := makeReq(t, tc.method, map[string]any{"name": tc.bodyName})
			h := http.Header{}
			h.Set("Mcp-Method", tc.method)
			h.Set("Mcp-Name", tc.header)
			resp := validateRoutingHeaders(req, h)
			if tc.wantErr {
				if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeHeaderMismatch {
					t.Fatalf("expected -32020, got %+v", resp)
				}
				return
			}
			if resp != nil {
				t.Fatalf("expected pass, got %+v", resp)
			}
		})
	}
}
