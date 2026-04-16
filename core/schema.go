package core

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// SchemaGenerator converts a Go value (typically a pointer to a struct) into
// a JSON Schema representation. The value is used only for type reflection —
// its fields are not read.
//
// Implementations should return a JSON-encoded schema suitable for
// ToolDef.InputSchema / OutputSchema (type: "object" with properties, required, etc.).
//
// Example usage:
//
//	schema := sg(new(MyInput))  // reflects on MyInput struct tags
type SchemaGenerator func(v any) json.RawMessage

// defaultSchemaGen is the package-level schema generator used by TypedTool.
// Defaults to invopop/jsonschema with MCP-friendly settings.
var defaultSchemaGen SchemaGenerator = newInvopopSchemaGen()

// SetSchemaGenerator replaces the default schema generator used by TypedTool
// and TextTool. Call this once at program startup before registering tools.
//
//	core.SetSchemaGenerator(myCustomGen)
func SetSchemaGenerator(sg SchemaGenerator) {
	defaultSchemaGen = sg
}

// GenerateSchema derives a JSON Schema from a Go type using the current
// schema generator. Panics if no generator is set.
func GenerateSchema[T any]() json.RawMessage {
	if defaultSchemaGen == nil {
		panic("mcpkit: no SchemaGenerator set — call core.SetSchemaGenerator or use the default")
	}
	return defaultSchemaGen(new(T))
}

// newInvopopSchemaGen creates a SchemaGenerator backed by invopop/jsonschema.
// Configured to produce clean MCP-compatible schemas:
//   - No $id (Anonymous)
//   - No $defs/$ref (DoNotReference) — all types inlined
//   - No additionalProperties restriction (AllowAdditionalProperties)
//   - Required fields inferred from omitempty absence (Go JSON convention)
func newInvopopSchemaGen() SchemaGenerator {
	r := &jsonschema.Reflector{
		Anonymous:                 true,
		DoNotReference:            true,
		AllowAdditionalProperties: true,
	}
	return func(v any) json.RawMessage {
		s := r.Reflect(v)
		s.Version = ""
		s.ID = ""
		data, err := json.Marshal(s)
		if err != nil {
			panic(fmt.Sprintf("mcpkit: failed to marshal schema for %T: %v", v, err))
		}
		return data
	}
}
