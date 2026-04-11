package server_test

// End-to-end tests for server-side JSON Schema validation (#184).
// Covers tool and prompt argument validation, registration-time schema
// compilation, and the WithSchemaValidation(false) opt-out. All tests
// run through the public server.Register / Server.Dispatch API so that
// failures would be visible to any real client.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSchemaTestServer returns a fresh initialized server for validation tests.
// Bypasses the full handshake by using Dispatch directly, which is the path
// every transport flows through.
func newSchemaTestServer(t *testing.T, opts ...server.Option) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "schema-test", Version: "1.0"}, opts...)
	// Run the initialize handshake so subsequent Dispatch calls don't hit
	// the "not initialized" fast path.
	initReq := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	}
	resp := srv.Dispatch(context.Background(), initReq)
	require.Nil(t, resp.Error, "initialize should succeed")
	notif := &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	srv.Dispatch(context.Background(), notif)
	return srv
}

// callTool issues a tools/call request via Dispatch and returns the response.
func callTool(t *testing.T, srv *server.Server, name string, args any) *core.Response {
	t.Helper()
	argsRaw, err := json.Marshal(args)
	require.NoError(t, err)
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": json.RawMessage(argsRaw),
	})
	require.NoError(t, err)
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params:  params,
	}
	return srv.Dispatch(context.Background(), req)
}

// getPrompt issues a prompts/get request via Dispatch and returns the response.
func getPrompt(t *testing.T, srv *server.Server, name string, args map[string]any) *core.Response {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	require.NoError(t, err)
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "prompts/get",
		Params:  params,
	}
	return srv.Dispatch(context.Background(), req)
}

// okHandler is a trivial tool handler that returns "ok". Used whenever the
// test only cares about whether the call reached the handler.
func okHandler(_ context.Context, _ core.ToolRequest) (core.ToolResult, error) {
	return core.TextResult("ok"), nil
}

// okPromptHandler is the prompt-side analogue of okHandler.
func okPromptHandler(_ context.Context, _ core.PromptRequest) (core.PromptResult, error) {
	return core.PromptResult{Description: "ok"}, nil
}

// TestRegisterToolInvalidSchemaPanics verifies that a malformed InputSchema
// fails fast at registration time via panic. Invalid schemas in the binary
// are programmer errors — catching them at startup is better than at first
// call, which is why RegisterTool panics rather than silently ignoring.
func TestRegisterToolInvalidSchemaPanics(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	assert.Panics(t, func() {
		srv.RegisterTool(core.ToolDef{
			Name: "bad",
			// "type" must be a string or array of strings, not a map.
			InputSchema: map[string]any{"type": map[string]any{"nope": 1}},
		}, okHandler)
	}, "RegisterTool with malformed schema should panic")
}

// TestRegisterPromptInvalidSchemaPanics mirrors the tool test for prompts.
// Ensures that invalid per-argument schemas also fail fast at registration.
func TestRegisterPromptInvalidSchemaPanics(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	assert.Panics(t, func() {
		srv.RegisterPrompt(core.PromptDef{
			Name: "bad",
			Arguments: []core.PromptArgument{{
				Name:   "x",
				Schema: map[string]any{"type": map[string]any{"nope": 1}},
			}},
		}, okPromptHandler)
	}, "RegisterPrompt with malformed argument schema should panic")
}

// TestToolValidateTypeMismatch verifies that an argument with the wrong
// JSON type produces a -32602 error with a structured errors list pointing
// at the offending field path.
func TestToolValidateTypeMismatch(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterTool(core.ToolDef{
		Name: "typed",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"age": map[string]any{"type": "integer"},
			},
		},
	}, okHandler)

	resp := callTool(t, srv, "typed", map[string]any{"age": "not a number"})
	require.NotNil(t, resp.Error, "expected validation error")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "argument validation failed")

	// The error data should be a ValidationErrors struct with a path
	// pointing at /age. Round-trip through JSON since Data is `any`.
	raw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var ve server.ValidationErrors
	require.NoError(t, json.Unmarshal(raw, &ve))
	require.NotEmpty(t, ve.Errors)
	found := false
	for _, e := range ve.Errors {
		if e.Path == "/age" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected an error at /age, got %+v", ve.Errors)
}

// TestToolValidateMissingRequired verifies that missing required fields
// are reported so agents know what to supply on the retry.
func TestToolValidateMissingRequired(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterTool(core.ToolDef{
		Name: "req",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}, okHandler)

	resp := callTool(t, srv, "req", map[string]any{"other": "value"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)
}

// TestToolValidateSuccess is the happy path — a valid call reaches the
// handler and returns its result. If validation were incorrectly rejecting
// valid inputs, this would catch it.
func TestToolValidateSuccess(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterTool(core.ToolDef{
		Name: "ok",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}, okHandler)

	resp := callTool(t, srv, "ok", map[string]any{"name": "alice"})
	require.Nil(t, resp.Error, "valid args should not produce an error")
	assert.NotNil(t, resp.Result)
}

// TestToolValidateNoSchema verifies that tools which declare no schema
// bypass validation entirely — the handler sees whatever the client sent.
// This is the backward-compat path for tools written before #184.
func TestToolValidateNoSchema(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterTool(core.ToolDef{Name: "noschema"}, okHandler)

	// Any shape of arguments should reach the handler.
	resp := callTool(t, srv, "noschema", map[string]any{"anything": "goes"})
	require.Nil(t, resp.Error)
}

// TestToolValidateNullArguments verifies that arguments: null (the common
// wire shape for "no args") passes validation against an empty object
// schema. This is the regression that broke the streaming tests during
// implementation — worth an explicit assertion.
func TestToolValidateNullArguments(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterTool(core.ToolDef{
		Name:        "noargs",
		InputSchema: map[string]any{"type": "object"},
	}, okHandler)

	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"noargs","arguments":null}`),
	}
	resp := srv.Dispatch(context.Background(), req)
	require.Nil(t, resp.Error, "null arguments should validate against object schema")
}

// TestPromptValidateTypeMismatch verifies that prompt argument validation
// enforces declared schemas. The error path is prefixed with the argument
// name so the client can tell which field failed.
func TestPromptValidateTypeMismatch(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterPrompt(core.PromptDef{
		Name: "greet",
		Arguments: []core.PromptArgument{{
			Name:   "count",
			Schema: map[string]any{"type": "integer", "minimum": 0},
		}},
	}, okPromptHandler)

	// Send a string where an integer is expected
	resp := getPrompt(t, srv, "greet", map[string]any{"count": "twelve"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)

	raw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var ve server.ValidationErrors
	require.NoError(t, json.Unmarshal(raw, &ve))
	require.NotEmpty(t, ve.Errors)
	// Path should start with /count
	foundCount := false
	for _, e := range ve.Errors {
		if strings.HasPrefix(e.Path, "/count") {
			foundCount = true
		}
	}
	assert.True(t, foundCount, "expected path /count prefix, got %+v", ve.Errors)
}

// TestPromptValidateMultipleErrors verifies that validation failures on
// multiple arguments are all collected into a single error response,
// not reported one at a time. Agents correcting their call benefit from
// seeing every violation up front.
func TestPromptValidateMultipleErrors(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterPrompt(core.PromptDef{
		Name: "multi",
		Arguments: []core.PromptArgument{
			{Name: "age", Schema: map[string]any{"type": "integer"}},
			{Name: "city", Schema: map[string]any{"type": "string"}},
		},
	}, okPromptHandler)

	resp := getPrompt(t, srv, "multi", map[string]any{
		"age":  "thirty",
		"city": 42,
	})
	require.NotNil(t, resp.Error)
	raw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var ve server.ValidationErrors
	require.NoError(t, json.Unmarshal(raw, &ve))
	// Both failing arguments should be represented
	haveAge, haveCity := false, false
	for _, e := range ve.Errors {
		if strings.HasPrefix(e.Path, "/age") {
			haveAge = true
		}
		if strings.HasPrefix(e.Path, "/city") {
			haveCity = true
		}
	}
	assert.True(t, haveAge && haveCity, "expected both /age and /city errors, got %+v", ve.Errors)
}

// TestPromptValidateMixedSchemas verifies that prompt arguments without
// a Schema bypass validation while siblings with a Schema are still checked.
// This is the common case: one typed field, several free-form strings.
func TestPromptValidateMixedSchemas(t *testing.T) {
	srv := newSchemaTestServer(t)
	srv.RegisterPrompt(core.PromptDef{
		Name: "mixed",
		Arguments: []core.PromptArgument{
			{Name: "priority", Schema: map[string]any{"type": "integer"}},
			{Name: "comment"}, // no schema, no validation
		},
	}, okPromptHandler)

	// comment can be any type; priority must be int
	resp := getPrompt(t, srv, "mixed", map[string]any{
		"priority": 5,
		"comment":  "anything goes here",
	})
	require.Nil(t, resp.Error)
}

// TestWithSchemaValidationDisabled verifies that WithSchemaValidation(false)
// bypasses call-time validation so handlers see raw args unchecked. Schema
// compilation at registration still runs (malformed schemas still fail
// fast), only the dispatch-time check is skipped.
func TestWithSchemaValidationDisabled(t *testing.T) {
	srv := newSchemaTestServer(t, server.WithSchemaValidation(false))
	srv.RegisterTool(core.ToolDef{
		Name: "strict",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}, okHandler)

	// This would normally fail validation (missing required "name"), but
	// with validation disabled the call should reach the handler.
	resp := callTool(t, srv, "strict", map[string]any{})
	require.Nil(t, resp.Error, "validation should be bypassed")
}

// TestSchema2020_12Conformance is the #142 verification test. It registers
// a tool with a schema that uses JSON Schema 2020-12-specific features —
// $schema, $defs, $ref, prefixItems, dependentRequired — and verifies that:
//
//  1. Registration succeeds (the compiler accepts 2020-12).
//  2. Valid arguments reach the handler.
//  3. Invalid arguments produce -32602 with a structured error.
//
// This locks in mcpkit's claim of "JSON Schema 2020-12 support" against
// both compile-time acceptance AND runtime enforcement.
func TestSchema2020_12Conformance(t *testing.T) {
	srv := newSchemaTestServer(t)
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"$defs": map[string]any{
			"positiveInt": map[string]any{
				"type":    "integer",
				"minimum": 1,
			},
		},
		"properties": map[string]any{
			"id":    map[string]any{"$ref": "#/$defs/positiveInt"},
			"email": map[string]any{"type": "string", "format": "email"},
			"coords": map[string]any{
				"type": "array",
				"prefixItems": []any{
					map[string]any{"type": "number"}, // latitude
					map[string]any{"type": "number"}, // longitude
				},
			},
		},
		"required":          []string{"id"},
		"dependentRequired": map[string]any{"email": []string{"id"}},
	}
	srv.RegisterTool(core.ToolDef{Name: "conform", InputSchema: schema}, okHandler)

	// Happy path — exercises $ref, prefixItems, and dependentRequired.
	ok := callTool(t, srv, "conform", map[string]any{
		"id":     42,
		"email":  "alice@example.com",
		"coords": []any{37.7749, -122.4194},
	})
	require.Nil(t, ok.Error, "valid 2020-12 input should reach the handler")

	// id must be a positive integer per $ref
	bad := callTool(t, srv, "conform", map[string]any{"id": -5})
	require.NotNil(t, bad.Error)
	assert.Equal(t, core.ErrCodeInvalidParams, bad.Error.Code)
}
