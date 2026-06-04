package core

import "encoding/json"

// Schema patching — fluent editor over the reflected JSON Schema that
// TypedTool emits. Lets handler registrations express "tweak these few
// fields" without restating the whole shape as a raw map[string]any.
//
// Motivating context: `WithInputSchemaOverride` / `WithOutputSchemaOverride`
// (and the equivalent fields on `ext/ui.TypedAppToolConfig`) bypass
// reflection entirely and ship a verbatim schema. That's the right
// escape hatch for cases where reflection genuinely can't produce the
// shape needed (nullable types, anyOf composition, JSON Schema 2020-12
// features). But the vast majority of override use is "add per-field
// description / default / min / max" — work reflection ALREADY did 95%
// of, where the override forces the caller to re-state every other field.
//
// Patch starts from the reflected schema and edits it. Override stays
// available for the genuinely irreducible cases. When both Patch and
// Override are configured on the same option set, Override wins
// (documented on the With* options).

// SchemaBuilder is a fluent editor over a JSON Schema object. Created
// internally during TypedTool registration when a Patch option is set;
// the user-supplied patch function receives a builder backed by the
// reflected schema, edits it, and returns. The builder writes through
// to the underlying map; there's no commit / apply step.
type SchemaBuilder struct {
	schema map[string]any
}

// newSchemaBuilder wraps a raw map[string]any schema as a builder.
// Promises `schema["properties"]` exists as a map; creates it on
// demand if reflection emitted a property-less schema.
func newSchemaBuilder(schema map[string]any) *SchemaBuilder {
	if _, ok := schema["properties"].(map[string]any); !ok {
		schema["properties"] = map[string]any{}
	}
	return &SchemaBuilder{schema: schema}
}

// Prop returns a PropertyBuilder for the named property. If the
// property doesn't exist in the reflected schema yet, an empty object
// schema is inserted and returned for editing — patches are additive,
// not strict.
func (s *SchemaBuilder) Prop(name string) *PropertyBuilder {
	props := s.schema["properties"].(map[string]any)
	existing, ok := props[name].(map[string]any)
	if !ok {
		existing = map[string]any{}
		props[name] = existing
	}
	return &PropertyBuilder{schema: existing, parent: s, name: name}
}

// Require adds names to the `required` array idempotently. Duplicate
// adds are silently dropped; entries not in `properties` are still
// added — the JSON Schema spec accepts it.
func (s *SchemaBuilder) Require(names ...string) *SchemaBuilder {
	current, _ := s.schema["required"].([]string)
	// JSON unmarshaling lands required as []any; tolerate that on top of
	// reflection's []string output.
	if current == nil {
		if anyArr, ok := s.schema["required"].([]any); ok {
			for _, e := range anyArr {
				if str, ok := e.(string); ok {
					current = append(current, str)
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(current))
	for _, n := range current {
		seen[n] = struct{}{}
	}
	for _, n := range names {
		if _, ok := seen[n]; ok {
			continue
		}
		current = append(current, n)
		seen[n] = struct{}{}
	}
	s.schema["required"] = current
	return s
}

// Raw returns the underlying schema map. Mutations land on the
// original — use sparingly for surgical edits the builder doesn't
// express directly.
func (s *SchemaBuilder) Raw() map[string]any { return s.schema }

// PropertyBuilder is a fluent editor over one property's schema. All
// setter methods chain.
type PropertyBuilder struct {
	schema map[string]any
	parent *SchemaBuilder
	name   string
}

// Type sets the JSON Schema `type` field ("string", "number",
// "integer", "boolean", "object", "array", "null"). Replaces any
// previous type value.
func (p *PropertyBuilder) Type(t string) *PropertyBuilder {
	p.schema["type"] = t
	return p
}

// Desc sets the `description` field on the property.
func (p *PropertyBuilder) Desc(d string) *PropertyBuilder {
	p.schema["description"] = d
	return p
}

// Default sets the `default` field. The value should match the
// property's JSON type.
func (p *PropertyBuilder) Default(v any) *PropertyBuilder {
	p.schema["default"] = v
	return p
}

// Min sets `minimum` (number-typed properties).
func (p *PropertyBuilder) Min(n float64) *PropertyBuilder {
	p.schema["minimum"] = n
	return p
}

// Max sets `maximum` (number-typed properties).
func (p *PropertyBuilder) Max(n float64) *PropertyBuilder {
	p.schema["maximum"] = n
	return p
}

// MinLength sets `minLength` (string-typed properties).
func (p *PropertyBuilder) MinLength(n int) *PropertyBuilder {
	p.schema["minLength"] = n
	return p
}

// MaxLength sets `maxLength` (string-typed properties).
func (p *PropertyBuilder) MaxLength(n int) *PropertyBuilder {
	p.schema["maxLength"] = n
	return p
}

// Enum sets the `enum` field. Values are stored verbatim as a JSON
// array; the property's `type` should be set separately when needed.
func (p *PropertyBuilder) Enum(values ...any) *PropertyBuilder {
	p.schema["enum"] = values
	return p
}

// Pattern sets the `pattern` field (string-typed properties), a
// regex constraint per JSON Schema spec.
func (p *PropertyBuilder) Pattern(re string) *PropertyBuilder {
	p.schema["pattern"] = re
	return p
}

// Required adds this property's name to the parent schema's `required`
// array. Idempotent. Equivalent to `builder.Require(p.name)`.
func (p *PropertyBuilder) Required() *PropertyBuilder {
	p.parent.Require(p.name)
	return p
}

// Replace swaps the property's entire schema with the supplied map.
// Use for nullable (anyOf with null), record-of-union, and any JSON
// Schema 2020-12 feature the typed setters don't express. The map's
// keys land at the property's top level — caller is responsible for
// the wire shape.
func (p *PropertyBuilder) Replace(schema map[string]any) *PropertyBuilder {
	// Mutate in place so PropertyBuilder's reference stays live.
	for k := range p.schema {
		delete(p.schema, k)
	}
	for k, v := range schema {
		p.schema[k] = v
	}
	return p
}

// End returns the parent SchemaBuilder. Use when a chain wants to
// step back up to add more properties or call Require with multiple
// names — purely a style choice; sequential `s.Prop(...)` calls work
// without it.
func (p *PropertyBuilder) End() *SchemaBuilder { return p.parent }

// --- TypedToolOption integration -------------------------------------------

// WithInputSchemaPatch returns a TypedToolOption that runs fn against
// the reflected input schema after generation. The patch sees a
// SchemaBuilder backed by the live schema map and edits in place.
//
// Use the patch path when the diff between reflection and the desired
// schema is "tweak a few fields" (descriptions, defaults, min/max,
// enum, required). Use WithInputSchemaOverride when you genuinely
// want to start from a blank schema and stuff the entire shape in.
//
// Precedence: WithInputSchemaOverride wins over WithInputSchemaPatch
// when both are set on the same registration — Override is the
// stronger "replace entirely" intent and we don't want silent merging.
// Calling both on the same option set is almost certainly a bug; the
// godoc note here is the only signpost.
func WithInputSchemaPatch(fn func(*SchemaBuilder)) TypedToolOption {
	return func(c *typedToolConfig) { c.inputSchemaPatch = fn }
}

// WithOutputSchemaPatch is the symmetric mirror for the output schema.
// Behaves identically to WithInputSchemaPatch except it edits the
// schema generated for Out (or skipped when Out is string / ToolResult
// / ToolResponse — in those cases there's no output schema to patch
// and the function is silently skipped).
func WithOutputSchemaPatch(fn func(*SchemaBuilder)) TypedToolOption {
	return func(c *typedToolConfig) { c.outputSchemaPatch = fn }
}

// applyPatch runs fn against a schema in its json.RawMessage form,
// returning the patched bytes. Used by TypedTool after reflection.
// Returns the original bytes if the schema can't be parsed (defensive
// — the only callers feed it our own invopop-produced output).
func applyPatch(schema json.RawMessage, fn func(*SchemaBuilder)) json.RawMessage {
	if fn == nil || len(schema) == 0 {
		return schema
	}
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		return schema
	}
	fn(newSchemaBuilder(m))
	out, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return out
}

// applyPatchToAny runs fn against an OutputSchema field that the
// generator emits as `any` (typically a json.RawMessage or
// map[string]any). Mirrors applyPatch's defensive behavior.
func applyPatchToAny(schema any, fn func(*SchemaBuilder)) any {
	if fn == nil || schema == nil {
		return schema
	}
	switch v := schema.(type) {
	case json.RawMessage:
		return json.RawMessage(applyPatch(v, fn))
	case []byte:
		return json.RawMessage(applyPatch(v, fn))
	case map[string]any:
		fn(newSchemaBuilder(v))
		return v
	default:
		// Fall back to marshal/unmarshal round-trip.
		b, err := json.Marshal(schema)
		if err != nil {
			return schema
		}
		return json.RawMessage(applyPatch(b, fn))
	}
}
