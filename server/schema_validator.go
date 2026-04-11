package server

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidationError is one violation reported by schema validation.
// Path is a JSON Pointer to the offending value (e.g., "/age"),
// Keyword is the JSON Schema keyword that failed (e.g., "type", "format"),
// and Message is a human-readable description.
type ValidationError struct {
	Path    string `json:"path"`
	Keyword string `json:"keyword"`
	Message string `json:"message"`
}

// ValidationErrors is the payload returned in the error.data field of
// JSON-RPC -32602 responses when argument validation fails.
type ValidationErrors struct {
	Errors []ValidationError `json:"errors"`
}

// compiledSchema wraps a compiled JSON Schema for use at dispatch time.
// Compiled once at registration; used to validate many incoming requests.
type compiledSchema struct {
	schema *jsonschema.Schema
}

// compileSchema compiles a JSON Schema value (as stored on ToolDef.InputSchema
// or PromptArgument.Schema) into a validator. The schema is expected to be
// an `any` value that round-trips through JSON (map[string]any is typical).
//
// Network $ref resolution is disabled — all $refs must resolve within the
// advertised schema via $defs or fragment pointers. This prevents servers
// from unknowingly opening HTTP fetches to untrusted hosts at validation time.
//
// Returns a non-nil compiled schema on success, or an error if the schema
// is malformed.
func compileSchema(schemaValue any) (*compiledSchema, error) {
	if schemaValue == nil {
		return nil, nil
	}

	// Marshal to JSON bytes so we can re-parse through the jsonschema library.
	// This normalizes the representation (map[string]any, json.RawMessage,
	// user-defined structs, etc.) into a single canonical form.
	raw, err := json.Marshal(schemaValue)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}

	// Unmarshal into the loose form that jsonschema/v6 expects.
	var loose any
	if err := json.Unmarshal(raw, &loose); err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}

	// Use a compiler with a restrictive loader — refuses all remote fetches.
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noNetworkLoader{})

	// Register the schema under a synthetic URL so the compiler can reference it.
	const schemaURL = "mem://schema.json"
	if err := compiler.AddResource(schemaURL, loose); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}

	sch, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return &compiledSchema{schema: sch}, nil
}

// validate checks a raw JSON value against the compiled schema. Returns nil
// if the value conforms, or a ValidationErrors struct containing all
// violations otherwise.
//
// The raw argument is typically envelope.Arguments (json.RawMessage) from
// a tools/call request. nil/empty raw is treated as an empty object ({}).
func (c *compiledSchema) validate(raw json.RawMessage) *ValidationErrors {
	if c == nil || c.schema == nil {
		return nil
	}

	var value any
	// Treat missing, empty, or explicit null arguments as an empty object.
	// This matches the spec-level intent: tools that declare `{"type":"object"}`
	// but accept no arguments should pass validation when the client sends
	// `"arguments": null`, `"arguments": {}`, or omits the field entirely.
	if len(raw) == 0 || string(raw) == "null" {
		value = map[string]any{}
	} else if err := json.Unmarshal(raw, &value); err != nil {
		return &ValidationErrors{
			Errors: []ValidationError{{
				Path:    "/",
				Keyword: "parse",
				Message: "invalid JSON: " + err.Error(),
			}},
		}
	}

	if err := c.schema.Validate(value); err != nil {
		return convertValidationError(err)
	}
	return nil
}

// validateValue is like validate but takes a pre-decoded Go value.
// Used for prompt arguments that have already been unmarshaled from the
// request envelope into a map[string]any.
func (c *compiledSchema) validateValue(value any) *ValidationErrors {
	if c == nil || c.schema == nil {
		return nil
	}
	if err := c.schema.Validate(value); err != nil {
		return convertValidationError(err)
	}
	return nil
}

// convertValidationError turns a jsonschema.ValidationError into our
// ValidationErrors wire format. The jsonschema library reports nested
// errors via .Causes — we walk the tree and flatten leaf-level errors.
func convertValidationError(err error) *ValidationErrors {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return &ValidationErrors{
			Errors: []ValidationError{{
				Path:    "/",
				Keyword: "",
				Message: err.Error(),
			}},
		}
	}

	var out []ValidationError
	walkValidationErrors(ve, &out)
	if len(out) == 0 {
		// No leaf errors collected — fall back to the top-level message
		out = append(out, ValidationError{
			Path:    instanceLocationPath(ve),
			Keyword: "",
			Message: ve.Error(),
		})
	}
	return &ValidationErrors{Errors: out}
}

// walkValidationErrors flattens nested validation errors into a slice,
// collecting only leaf errors (those with no further causes). This gives
// callers a flat list of specific violations rather than a tree.
func walkValidationErrors(ve *jsonschema.ValidationError, out *[]ValidationError) {
	if len(ve.Causes) == 0 {
		// Leaf error — record it
		kind := ""
		if ve.ErrorKind != nil {
			kind = kindKeyword(fmt.Sprintf("%T", ve.ErrorKind))
		}
		*out = append(*out, ValidationError{
			Path:    instanceLocationPath(ve),
			Keyword: kind,
			Message: ve.Error(),
		})
		return
	}
	for _, cause := range ve.Causes {
		walkValidationErrors(cause, out)
	}
}

// instanceLocationPath converts a jsonschema InstanceLocation ([]string)
// to a JSON Pointer string. Empty location becomes "/" (the root).
func instanceLocationPath(ve *jsonschema.ValidationError) string {
	if len(ve.InstanceLocation) == 0 {
		return "/"
	}
	return "/" + strings.Join(ve.InstanceLocation, "/")
}

// kindKeyword extracts the JSON Schema keyword from a jsonschema ErrorKind
// type name. The library uses types like "*jsonschema.kind.Type" — we strip
// the package prefix and lowercase the result for a stable keyword string.
func kindKeyword(typeName string) string {
	// Examples: "*kind.Type" → "type", "*kind.Format" → "format"
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typeName = typeName[idx+1:]
	}
	return strings.ToLower(typeName)
}

// noNetworkLoader is a jsonschema Loader that refuses all remote fetches.
// This is the default loader for mcpkit — callers who want network $refs
// must opt in via a separate mechanism (not currently supported).
type noNetworkLoader struct{}

// Load implements the jsonschema.URLLoader interface. It always returns an
// error, effectively forbidding any $ref that doesn't resolve within the
// compiled schema's own $defs.
func (noNetworkLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("network schema loading is disabled (attempted %q)", url)
}

// Ensure noNetworkLoader satisfies the expected interface.
var _ jsonschema.URLLoader = noNetworkLoader{}

// drain is a helper for tests that may need to exhaust a reader. Kept here
// to avoid importing io in test files unnecessarily.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
