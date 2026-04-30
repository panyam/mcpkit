package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TestCallToolWithInputs_Elicitation drives a minimal SEP-2322 round-trip
// through the public client API: server returns IncompleteResult asking
// for an elicitation, the DefaultInputHandler routes it to the client's
// registered elicitationHandler, the client retries with inputResponses,
// and the server's second invocation returns a complete ToolResult.
func TestCallToolWithInputs_Elicitation(t *testing.T) {
	srv := mrtrTestServer(t)

	c, _ := connectMRTRClient(t, srv,
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{
				Action: "accept",
				Content: map[string]any{
					"name": "Alice",
				},
			}, nil
		}),
	)

	res, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		client.DefaultInputHandler(c),
	)
	if err != nil {
		t.Fatalf("CallToolWithInputs: %v", err)
	}
	if res.IsIncomplete() || res.IsTask() {
		t.Fatalf("expected sync result, got %+v", res)
	}
	if res.Sync == nil || len(res.Sync.Content) == 0 {
		t.Fatalf("missing content; result=%+v", res)
	}
	text := res.Sync.Content[0].Text
	if !strings.Contains(text, "Alice") {
		t.Errorf("text=%q, want greeting with Alice", text)
	}
}

// TestCallToolWithInputs_MaxRounds verifies the loop bails out cleanly
// when the server keeps responding with IncompleteResult past the
// configured cap. Without this guard, a misbehaving server could pin
// the client in an infinite retry loop.
func TestCallToolWithInputs_MaxRounds(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-cap", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "always_incomplete",
			Description: "Always asks for more input",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return ctx.RequestInput(core.InputRequests{
				"loop": core.InputRequest{Method: "elicitation/create", Params: json.RawMessage(`{}`)},
			})
		},
	)

	c, _ := connectMRTRClient(t, srv,
		client.WithElicitationHandler(func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{}}, nil
		}),
	)

	_, err := client.CallToolWithInputs(context.Background(), c,
		"always_incomplete", map[string]any{},
		client.DefaultInputHandler(c),
		client.WithMaxMRTRRounds(3),
	)
	if !errors.Is(err, client.ErrMRTRMaxRounds) {
		t.Fatalf("err = %v, want ErrMRTRMaxRounds", err)
	}
}

// TestCallToolWithInputs_HandlerError verifies that an InputHandler error
// surfaces through CallToolWithInputs without retrying. (e.g. user
// declined elicitation, sampling provider unavailable.)
func TestCallToolWithInputs_HandlerError(t *testing.T) {
	srv := mrtrTestServer(t)
	c, _ := connectMRTRClient(t, srv)

	wantErr := errors.New("user declined")
	_, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
			return nil, wantErr
		},
	)
	if err == nil || !strings.Contains(err.Error(), "user declined") {
		t.Errorf("err = %v, want propagated handler error", err)
	}
}

// TestParseToolCallResult_Incomplete verifies the wire-shape probe
// recognises the SEP-2322 result_type:"incomplete" discriminator and
// surfaces the IncompleteResult payload to callers using the bare
// ToolCall API.
func TestParseToolCallResult_Incomplete(t *testing.T) {
	srv := mrtrTestServer(t)
	c, _ := connectMRTRClient(t, srv)

	// Bare ToolCall — should surface Incomplete on round 1.
	res, err := client.ToolCall(c, "test_tool_with_elicitation", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if !res.IsIncomplete() {
		t.Fatalf("expected Incomplete; got %+v", res)
	}
	if res.Incomplete.InputRequests["user_name"].Method != "elicitation/create" {
		t.Errorf("unexpected method: %+v", res.Incomplete.InputRequests)
	}
	if res.Incomplete.RequestState == "" {
		t.Error("expected non-empty requestState on Incomplete")
	}
}

// --- helpers ---

func mrtrTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-client-test", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_elicitation",
			Description: "Asks for user name then greets them",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)
	return srv
}

func connectMRTRClient(t *testing.T, srv *server.Server, opts ...client.ClientOption) (*client.Client, *httptest.Server) {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mrtr-client-test", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, ts
}
