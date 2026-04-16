package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	core "github.com/panyam/mcpkit/core"

	"github.com/invopop/jsonschema"
)

// schemaReflector is the package-level reflector used to derive JSON Schemas
// from Go struct types. Configured to produce clean MCP-compatible schemas:
// no $id, no $defs/$ref, no additionalProperties restriction.
var schemaReflector = &jsonschema.Reflector{
	Anonymous:                 true, // no auto-generated $id
	DoNotReference:            true, // inline all types (no $defs/$ref)
	AllowAdditionalProperties: true, // omit additionalProperties (opposite of go-sdk)
}

// TypedTool creates a Tool with InputSchema (and optionally OutputSchema) auto-derived
// from Go struct types. The handler receives typed input — no manual Bind() needed.
//
// The Out type parameter controls output behavior:
//   - string: handler returns text, wrapped via core.TextResult. No OutputSchema.
//   - core.ToolResult: handler returns a ToolResult directly. No OutputSchema.
//   - any struct: handler returns typed data. OutputSchema auto-derived from Out.
//     Result uses core.StructuredResult with JSON text fallback.
//
// Example (text output):
//
//	srv.Register(server.TextTool[SearchInput]("search", "Search books",
//	    func(ctx core.ToolContext, input SearchInput) (string, error) {
//	        return fmt.Sprintf("found %d books", len(results)), nil
//	    },
//	))
//
// Example (structured output):
//
//	srv.Register(server.TypedTool[SearchInput, SearchOutput]("search", "Search books",
//	    func(ctx core.ToolContext, input SearchInput) (SearchOutput, error) {
//	        return SearchOutput{Results: results, Total: len(results)}, nil
//	    },
//	))
func TypedTool[In, Out any](name, desc string,
	handler func(ctx core.ToolContext, input In) (Out, error),
	opts ...TypedToolOption,
) Tool {
	cfg := typedToolConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	inputSchema := generateSchema[In]()

	var outputSchema any
	outType := reflect.TypeOf((*Out)(nil)).Elem()
	isStringOut := outType.Kind() == reflect.String
	isToolResultOut := outType == reflect.TypeOf(core.ToolResult{})
	if !isStringOut && !isToolResultOut {
		outputSchema = generateSchema[Out]()
	}

	def := core.ToolDef{
		Name:         name,
		Description:  desc,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		Annotations:  cfg.annotations,
		Meta:         cfg.meta,
		Timeout:      cfg.timeout,
	}

	wrappedHandler := func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
		var input In
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return core.ErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		out, err := handler(ctx, input)
		if err != nil {
			return core.ToolResult{}, err
		}
		return wrapOutput(out, isStringOut, isToolResultOut)
	}

	return Tool{ToolDef: def, Handler: wrappedHandler}
}

// TextTool creates a Tool with auto-derived InputSchema where the handler returns
// a string. This is sugar for TypedTool[In, string] — the most common case.
//
// Example:
//
//	srv.Register(server.TextTool[GreetInput]("greet", "Say hello",
//	    func(ctx core.ToolContext, input GreetInput) (string, error) {
//	        return "Hello, " + input.Name, nil
//	    },
//	))
func TextTool[In any](name, desc string,
	handler func(ctx core.ToolContext, input In) (string, error),
	opts ...TypedToolOption,
) Tool {
	return TypedTool[In, string](name, desc, handler, opts...)
}

// TypedToolOption configures optional fields on a TypedTool.
type TypedToolOption func(*typedToolConfig)

type typedToolConfig struct {
	annotations map[string]any
	meta        *core.ToolMeta
	timeout     time.Duration
}

// WithToolAnnotations sets the Annotations field on the generated ToolDef.
func WithToolAnnotations(a map[string]any) TypedToolOption {
	return func(c *typedToolConfig) { c.annotations = a }
}

// WithToolMeta sets the Meta field on the generated ToolDef.
func WithToolMeta(m *core.ToolMeta) TypedToolOption {
	return func(c *typedToolConfig) { c.meta = m }
}

// WithTypedToolTimeout sets a per-tool execution timeout on the generated ToolDef.
func WithTypedToolTimeout(d time.Duration) TypedToolOption {
	return func(c *typedToolConfig) { c.timeout = d }
}

// generateSchema derives a JSON Schema from a Go type using struct tag reflection.
// Returns a json.RawMessage suitable for ToolDef.InputSchema / OutputSchema.
//
// Struct tags recognized:
//   - json:"name"           → property name (omitempty → optional, else required)
//   - jsonschema:"..."      → description, enum, min/max, pattern, etc.
//
// The generated schema omits $schema, $id, and additionalProperties.
func generateSchema[T any]() json.RawMessage {
	s := schemaReflector.Reflect(new(T))
	// Strip top-level fields that MCP tool schemas don't need.
	s.Version = ""
	s.ID = ""
	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("mcpkit: failed to marshal schema for %T: %v", new(T), err))
	}
	return data
}

// wrapOutput converts a typed handler output into a core.ToolResult.
func wrapOutput[Out any](out Out, isString, isToolResult bool) (core.ToolResult, error) {
	if isString {
		// Out is string — wrap as text result.
		return core.TextResult(any(out).(string)), nil
	}
	if isToolResult {
		// Out is core.ToolResult — passthrough.
		return any(out).(core.ToolResult), nil
	}
	// Out is a struct — wrap as structured result with JSON text fallback.
	data, err := json.Marshal(out)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("failed to marshal output: %v", err)), nil
	}
	return core.StructuredResult(string(data), out), nil
}
