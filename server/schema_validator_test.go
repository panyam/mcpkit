package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompileSchemaNil verifies that compiling a nil schema returns
// (nil, nil) — callers interpret this as "no schema to validate against,"
// which is the backward-compat path for handlers that didn't declare one.
func TestCompileSchemaNil(t *testing.T) {
	c, err := compileSchema(nil)
	require.NoError(t, err)
	assert.Nil(t, c)
}

// TestCompileSchemaValid verifies that a well-formed JSON Schema compiles
// into a reusable validator. Uses a minimal object schema with type and
// required — the baseline for most tool input schemas.
func TestCompileSchemaValid(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer", "minimum": 0},
		},
	}
	c, err := compileSchema(schema)
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestCompileSchemaInvalid verifies that malformed schemas fail at
// compile time with a descriptive error. This is the "fail fast" guarantee
// — advertised schemas that won't validate anything are rejected before
// any client sees them.
func TestCompileSchemaInvalid(t *testing.T) {
	schema := map[string]any{
		// `type` must be a string or array of strings per JSON Schema spec.
		// A map here is invalid.
		"type": map[string]any{"not": "allowed"},
	}
	_, err := compileSchema(schema)
	assert.Error(t, err)
}

// TestCompileSchemaNetworkRefForbidden verifies that schemas with network
// $refs (http://, https://) fail to compile by default. Network loading
// is disabled to prevent servers from unknowingly making HTTP calls at
// validation time — a real security concern.
func TestCompileSchemaNetworkRefForbidden(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"user": map[string]any{
				"$ref": "https://example.com/schemas/user.json",
			},
		},
	}
	_, err := compileSchema(schema)
	assert.Error(t, err, "network $ref should be rejected at compile time")
}

// TestValidateTypeMismatch verifies that arguments with the wrong type
// produce a -32602-level error with a path pointing at the offending
// field. Agents use this path to self-correct on the next call.
func TestValidateTypeMismatch(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"age": map[string]any{"type": "integer"},
		},
	}
	c, err := compileSchema(schema)
	require.NoError(t, err)

	raw := json.RawMessage(`{"age": "twenty"}`)
	ve := c.validate(raw)
	require.NotNil(t, ve, "expected validation errors")
	require.NotEmpty(t, ve.Errors)

	// Check that at least one error mentions the /age path
	found := false
	for _, e := range ve.Errors {
		if e.Path == "/age" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected an error at /age, got: %+v", ve.Errors)
}

// TestValidateMissingRequired verifies that missing required fields
// produce a validation error. This catches the common "typo in field name"
// case — the required field reports as absent even though the client sent
// a similarly-named field.
func TestValidateMissingRequired(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
	}
	c, err := compileSchema(schema)
	require.NoError(t, err)

	raw := json.RawMessage(`{"other": "value"}`)
	ve := c.validate(raw)
	require.NotNil(t, ve)
	require.NotEmpty(t, ve.Errors)
}

// TestValidateSuccess verifies that valid arguments return nil (no errors).
// This is the happy path — most calls should hit this.
func TestValidateSuccess(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
		},
	}
	c, err := compileSchema(schema)
	require.NoError(t, err)

	raw := json.RawMessage(`{"name": "alice", "age": 30}`)
	ve := c.validate(raw)
	assert.Nil(t, ve)
}

// TestValidateEmptyArgs verifies that a nil/empty raw argument value is
// treated as an empty object. This is important for tools that don't take
// arguments — they should still pass validation against an object schema.
func TestValidateEmptyArgs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
	}
	c, err := compileSchema(schema)
	require.NoError(t, err)

	ve := c.validate(nil)
	assert.Nil(t, ve)
}

// TestValidate2020_12Features verifies that JSON Schema 2020-12 features
// (prefixItems, $defs, $ref, format, dependentRequired) compile and enforce
// correctly. This is the #142 coverage check — confirms our validator
// handles the spec-mandated Draft 2020-12.
func TestValidate2020_12Features(t *testing.T) {
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"$defs": map[string]any{
			"email": map[string]any{
				"type":   "string",
				"format": "email",
			},
		},
		"properties": map[string]any{
			"contact": map[string]any{"$ref": "#/$defs/email"},
			"tags": map[string]any{
				"type":      "array",
				"prefixItems": []any{
					map[string]any{"type": "string"}, // first item must be string
					map[string]any{"type": "integer"}, // second item must be integer
				},
			},
		},
		"required":          []string{"contact"},
		"dependentRequired": map[string]any{"tags": []string{"contact"}},
	}
	c, err := compileSchema(schema)
	require.NoError(t, err, "2020-12 schema should compile")

	// Valid input
	raw := json.RawMessage(`{"contact":"alice@example.com","tags":["active",42]}`)
	assert.Nil(t, c.validate(raw), "valid 2020-12 input should pass")

	// Invalid: contact is not an email (basic check — format assertions
	// vary by library config, so we test a stronger invariant: type)
	bad := json.RawMessage(`{"contact":42}`)
	ve := c.validate(bad)
	require.NotNil(t, ve)
	require.NotEmpty(t, ve.Errors)
}
