package client

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

const validationSchema = `{
  "type": "object",
  "properties": {
    "env":      {"type": "string", "enum": ["dev", "prod"]},
    "replicas": {"type": "integer"}
  },
  "required": ["env"]
}`

func elicitCreateRequest(t *testing.T, schema string) *core.Request {
	t.Helper()
	params, err := json.Marshal(core.ElicitationRequest{
		Message:         "configure",
		RequestedSchema: json.RawMessage(schema),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "elicitation/create",
		Params:  core.NewRawJSON(params),
	}
}

func clientReturning(res core.ElicitationResult, opts ...ClientOption) *Client {
	all := append([]ClientOption{
		WithElicitationHandler(func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
			return res, nil
		}),
	}, opts...)
	return NewClient("http://example.invalid", core.ClientInfo{Name: "t", Version: "1"}, all...)
}

func TestElicitationValidationRejectsWrongType(t *testing.T) {
	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"env": "dev", "replicas": "two"},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

	if resp.Error == nil {
		t.Fatal("expected an error response for a string in an integer property")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
	raw, _ := json.Marshal(resp.Error.Data)
	var ve core.ValidationErrors
	if err := json.Unmarshal(raw, &ve); err != nil || len(ve.Errors) == 0 {
		t.Fatalf("expected structured ValidationErrors in error.data, got %s", raw)
	}
	if ve.Errors[0].Path != "/replicas" {
		t.Errorf("path = %q, want /replicas", ve.Errors[0].Path)
	}
}

func TestElicitationValidationRejectsValueOutsideEnum(t *testing.T) {
	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"env": "staging"},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

	if resp.Error == nil {
		t.Fatal("expected an error response for a value outside the declared enum")
	}
}

func TestElicitationValidationRejectsMissingRequired(t *testing.T) {
	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"replicas": 3},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

	if resp.Error == nil {
		t.Fatal("expected an error response when a required property is absent")
	}
}

func TestElicitationValidationAcceptsConformingContent(t *testing.T) {
	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"env": "prod", "replicas": 3},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
}

// A default supplied by SEP-1034 must be able to satisfy `required`, which only
// works if validation runs after the merge rather than before it.
func TestElicitationValidationRunsAfterDefaultsMerge(t *testing.T) {
	const schema = `{
      "type": "object",
      "properties": {"env": {"type": "string", "default": "dev"}},
      "required": ["env"]
    }`

	c := clientReturning(core.ElicitationResult{Action: "accept", Content: map[string]any{}})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, schema))

	if resp.Error != nil {
		t.Fatalf("default should have satisfied required, got: %s", resp.Error.Message)
	}
	var out core.ElicitationResult
	if err := resp.ResultAs(&out); err != nil {
		t.Fatal(err)
	}
	if out.Content["env"] != "dev" {
		t.Errorf("env = %v, want dev", out.Content["env"])
	}
}

// Content is undefined for decline and cancel, so there is nothing to validate.
func TestElicitationValidationSkippedWhenNotAccepted(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		c := clientReturning(core.ElicitationResult{
			Action:  action,
			Content: map[string]any{"replicas": "not-an-integer"},
		})

		resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

		if resp.Error != nil {
			t.Errorf("action %q: unexpected error: %s", action, resp.Error.Message)
		}
	}
}

// The defect is the server's, and the user answered in good faith, so a schema
// that cannot be compiled must not block their input.
func TestElicitationValidationSkippedOnUncompilableSchema(t *testing.T) {
	// Valid JSON, invalid JSON Schema: `type` must be a string or array.
	const badSchema = `{"type": "object", "properties": {"env": {"type": 42}}}`

	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"anything": true},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, badSchema))

	if resp.Error != nil {
		t.Errorf("expected pass-through, got: %s", resp.Error.Message)
	}
}

// Schema bytes that are not valid JSON cannot arrive over the wire: they would
// make the enclosing JSON-RPC message unparseable, so dispatch would reject it
// long before the handler runs. The guard is exercised directly instead.
func TestValidateElicitationContentGuardsUnparseableSchema(t *testing.T) {
	got := validateElicitationContent(json.RawMessage(`{"type": "object", `), map[string]any{"a": 1})
	if got != nil {
		t.Errorf("expected nil for unparseable schema, got %+v", got)
	}
}

func TestElicitationValidationOptOut(t *testing.T) {
	c := clientReturning(
		core.ElicitationResult{
			Action:  "accept",
			Content: map[string]any{"env": "dev", "replicas": "two"},
		},
		WithElicitationValidation(false),
	)

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, validationSchema))

	if resp.Error != nil {
		t.Fatalf("validation should be off: %s", resp.Error.Message)
	}
	var out core.ElicitationResult
	if err := resp.ResultAs(&out); err != nil {
		t.Fatal(err)
	}
	if out.Content["replicas"] != "two" {
		t.Errorf("replicas = %v, want the handler's value passed through", out.Content["replicas"])
	}
}

// No schema means nothing to validate against.
func TestElicitationValidationNoSchema(t *testing.T) {
	c := clientReturning(core.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"free": "form"},
	})

	resp := c.HandleServerRequestWithContext(context.Background(), elicitCreateRequest(t, ""))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
}
