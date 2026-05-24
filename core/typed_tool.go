package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// TypedToolResult holds the generated ToolDef and wrapped ToolHandler produced
// by TypedTool or TextTool. Use this to pass typed tool registrations to any
// registration API (server.Register, ext/ui.RegisterAppTool, etc.).
type TypedToolResult struct {
	ToolDef
	Handler ToolHandler
}

// TypedTool creates a ToolDef and ToolHandler with InputSchema (and optionally
// OutputSchema) auto-derived from Go struct types via the current SchemaGenerator.
// The handler receives typed input — no manual Bind() needed.
//
// The Out type parameter controls output behavior:
//   - string: handler returns text, wrapped via TextResult. No OutputSchema.
//   - ToolResult: handler returns a ToolResult directly. No OutputSchema.
//   - any struct: handler returns typed data. OutputSchema auto-derived from Out.
//     Result uses StructuredResult with JSON text fallback.
//
// Example (text output — use TextTool for convenience):
//
//	r := core.TypedTool[SearchInput, string]("search", "Search books",
//	    func(ctx core.ToolContext, input SearchInput) (string, error) {
//	        return fmt.Sprintf("found %d books", len(results)), nil
//	    },
//	)
//
// Example (structured output):
//
//	r := core.TypedTool[SearchInput, SearchOutput]("search", "Search books",
//	    func(ctx core.ToolContext, input SearchInput) (SearchOutput, error) {
//	        return SearchOutput{Results: results, Total: len(results)}, nil
//	    },
//	)
func TypedTool[In, Out any](name, desc string,
	handler func(ctx ToolContext, input In) (Out, error),
	opts ...TypedToolOption,
) TypedToolResult {
	cfg := typedToolConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	// Schema derivation. By default we reflect on In to produce a JSON Schema
	// from struct tags. WithInputSchemaOverride bypasses that reflection and
	// uses the caller-supplied schema verbatim — for cases where the required
	// schema uses JSON Schema 2020-12 features (conditional if/then/else,
	// $anchor, allOf/anyOf composition, etc.) that struct tags cannot express.
	// The handler still unmarshals into In, so caller is responsible for
	// keeping the override compatible with In's wire shape.
	inputSchema := cfg.inputSchemaOverride
	if inputSchema == nil {
		inputSchema = GenerateSchema[In]()
	}

	var outputSchema any
	outType := reflect.TypeOf((*Out)(nil)).Elem()
	isStringOut := outType.Kind() == reflect.String
	isToolResultOut := outType == reflect.TypeOf(ToolResult{})
	if !isStringOut && !isToolResultOut {
		outputSchema = GenerateSchema[Out]()
	}

	def := ToolDef{
		Name:           name,
		Description:    desc,
		InputSchema:    inputSchema,
		OutputSchema:   outputSchema,
		Annotations:    cfg.annotations,
		Meta:           cfg.meta,
		Timeout:        cfg.timeout,
		RequiredScopes: cfg.requiredScopes,
	}

	wrappedHandler := func(ctx ToolContext, req ToolRequest) (ToolResult, error) {
		var input In
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		out, err := handler(ctx, input)
		if err != nil {
			return ToolResult{}, err
		}
		return wrapOutput(out, isStringOut, isToolResultOut)
	}

	return TypedToolResult{ToolDef: def, Handler: wrappedHandler}
}

// TextTool creates a ToolDef and ToolHandler with auto-derived InputSchema where
// the handler returns a string. This is sugar for TypedTool[In, string].
//
// Example:
//
//	r := core.TextTool[GreetInput]("greet", "Say hello",
//	    func(ctx core.ToolContext, input GreetInput) (string, error) {
//	        return "Hello, " + input.Name, nil
//	    },
//	)
func TextTool[In any](name, desc string,
	handler func(ctx ToolContext, input In) (string, error),
	opts ...TypedToolOption,
) TypedToolResult {
	return TypedTool[In, string](name, desc, handler, opts...)
}

// TypedToolOption configures optional fields on a TypedTool.
type TypedToolOption func(*typedToolConfig)

type typedToolConfig struct {
	annotations         map[string]any
	meta                *ToolMeta
	timeout             time.Duration
	requiredScopes      []string
	inputSchemaOverride any
}

// WithToolAnnotations sets the Annotations field on the generated ToolDef.
func WithToolAnnotations(a map[string]any) TypedToolOption {
	return func(c *typedToolConfig) { c.annotations = a }
}

// WithToolMeta sets the Meta field on the generated ToolDef.
func WithToolMeta(m *ToolMeta) TypedToolOption {
	return func(c *typedToolConfig) { c.meta = m }
}

// WithTypedToolTimeout sets a per-tool execution timeout on the generated ToolDef.
func WithTypedToolTimeout(d time.Duration) TypedToolOption {
	return func(c *typedToolConfig) { c.timeout = d }
}

// WithToolRequiredScopes sets the RequiredScopes field on the generated ToolDef.
// When auth.NewToolScopeMiddleware is registered on the server, calls to this
// tool from clients without all of the named scopes are rejected at the
// transport layer with HTTP 403 + WWW-Authenticate per RFC 6750.
func WithToolRequiredScopes(scopes ...string) TypedToolOption {
	return func(c *typedToolConfig) { c.requiredScopes = scopes }
}

// WithInputSchemaOverride replaces the reflection-derived input schema with a
// caller-supplied schema. Use this when the tool's input shape needs JSON
// Schema 2020-12 features that struct tags cannot express — for example
// conditional validation (if/then/else), composition (allOf/anyOf), shared
// definitions with $anchor / $ref, or a custom $schema dialect declaration.
//
// The override is preserved as-is through tools/list (per ToolDef.InputSchema
// docs). The handler still unmarshals request arguments into In, so callers
// are responsible for keeping the override compatible with In's wire shape.
//
// Example:
//
//	type DeployInput struct {
//	    Env      string `json:"env"`
//	    Approver string `json:"approver,omitempty"`
//	}
//	schema := map[string]any{
//	    "type":     "object",
//	    "properties": map[string]any{ /* ... */ },
//	    "if":   map[string]any{"properties": map[string]any{"env": map[string]any{"const": "prod"}}},
//	    "then": map[string]any{"required": []string{"approver"}},
//	}
//	r := core.TypedTool[DeployInput, string]("deploy", "Deploy to env", handler,
//	    core.WithInputSchemaOverride(schema),
//	)
func WithInputSchemaOverride(schema any) TypedToolOption {
	return func(c *typedToolConfig) { c.inputSchemaOverride = schema }
}

// wrapOutput converts a typed handler output into a ToolResult.
func wrapOutput[Out any](out Out, isString, isToolResult bool) (ToolResult, error) {
	if isString {
		return TextResult(any(out).(string)), nil
	}
	if isToolResult {
		return any(out).(ToolResult), nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to marshal output: %v", err)), nil
	}
	return StructuredResult(string(data), out), nil
}
