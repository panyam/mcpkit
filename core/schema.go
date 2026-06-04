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
//   - $schema set to invopop's default draft URL (draft-2020-12) — emitted so
//     clients know which JSON Schema dialect to validate against. Other MCP
//     SDKs may emit different drafts (e.g. upstream's TS SDK emits draft-07
//     via zod-to-json-schema); both are valid self-describing schemas.
//   - No $id (Anonymous)
//   - No $defs/$ref (DoNotReference) — all types inlined
//   - No additionalProperties restriction (AllowAdditionalProperties) —
//     mcpkit's deliberate permissive default; tools that need strict
//     validation can override per-schema.
//   - Required fields inferred from omitempty absence (Go JSON convention)
func newInvopopSchemaGen() SchemaGenerator {
	r := &jsonschema.Reflector{
		Anonymous:                 true,
		DoNotReference:            true,
		AllowAdditionalProperties: true,
	}
	return func(v any) json.RawMessage {
		s := r.Reflect(v)
		// Keep s.Version (invopop default) so the produced schema is
		// self-describing — clients can pick the right validator. The
		// previous behavior of stripping it produced schemas without a
		// dialect declaration, which is honest only if clients have
		// out-of-band knowledge of the draft used.
		s.ID = ""
		data, err := json.Marshal(s)
		if err != nil {
			panic(fmt.Sprintf("mcpkit: failed to marshal schema for %T: %v", v, err))
		}
		return normalizeBareTrueSchemas(data)
	}
}

// normalizeBareTrueSchemas walks an emitted JSON Schema and replaces any
// bare-`true` property / item schemas with the empty object form `{}`.
//
// Why: invopop emits `"payload": true` for an `interface{}` / `any` field.
// That's spec-valid JSON Schema 2020-12 (means "anything goes"), but the
// MCP TypeScript SDK's zod validator rejects it on tools/list because it
// expects every property's schema to be an object. Upstream's `z.unknown()`
// emits `{}` (no constraints) which validates fine. Issue 548 Gap 2.
//
// Scope: only `properties.*` values, `items`, and `allOf` / `anyOf` /
// `oneOf` array elements are normalized. `additionalProperties: true`
// stays — there the boolean has a distinct semantic meaning (allow
// extra properties of any shape), not "schema for one property".
func normalizeBareTrueSchemas(data json.RawMessage) json.RawMessage {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return data
	}
	if normalized, changed := walkNormalizeBareTrue(root); changed {
		if b, err := json.Marshal(normalized); err == nil {
			return b
		}
	}
	return data
}

func walkNormalizeBareTrue(node any) (any, bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return node, false
	}
	changed := false
	if props, ok := m["properties"].(map[string]any); ok {
		for k, v := range props {
			if v == true {
				props[k] = map[string]any{}
				changed = true
				continue
			}
			if sub, sc := walkNormalizeBareTrue(v); sc {
				props[k] = sub
				changed = true
			}
		}
	}
	if items, ok := m["items"]; ok {
		if items == true {
			m["items"] = map[string]any{}
			changed = true
		} else if sub, sc := walkNormalizeBareTrue(items); sc {
			m["items"] = sub
			changed = true
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if arr, ok := m[key].([]any); ok {
			for i, e := range arr {
				if e == true {
					arr[i] = map[string]any{}
					changed = true
				} else if sub, sc := walkNormalizeBareTrue(e); sc {
					arr[i] = sub
					changed = true
				}
			}
		}
	}
	return m, changed
}
