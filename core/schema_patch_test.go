package core

import (
	"encoding/json"
	"testing"
)

// Schema patching tests. The patch path runs after reflection; the
// SchemaBuilder is a thin fluent wrapper over the underlying schema map.

func TestSchemaPatch_PropDescAndDefault(t *testing.T) {
	type In struct {
		URL string `json:"url"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("url").Desc("PDF URL or local file path").Default("https://example.com")
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "url")
	if got["description"] != "PDF URL or local file path" {
		t.Errorf("description = %v", got["description"])
	}
	if got["default"] != "https://example.com" {
		t.Errorf("default = %v", got["default"])
	}
	// Reflection's type assignment is preserved.
	if got["type"] != "string" {
		t.Errorf("type lost — want string, got %v", got["type"])
	}
}

func TestSchemaPatch_MinMaxOnNumber(t *testing.T) {
	type In struct {
		Offset float64 `json:"offset,omitempty"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("offset").Min(0).Max(512).Default(0)
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "offset")
	if got["minimum"].(float64) != 0 {
		t.Errorf("minimum = %v", got["minimum"])
	}
	if got["maximum"].(float64) != 512 {
		t.Errorf("maximum = %v", got["maximum"])
	}
}

func TestSchemaPatch_RequireSetsRequiredArray(t *testing.T) {
	type In struct {
		URL  string `json:"url,omitempty"`
		Data string `json:"data,omitempty"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Require("url", "data")
		}),
	)
	var schema map[string]any
	if err := json.Unmarshal(asRaw(r.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required missing or wrong type: %v", schema["required"])
	}
	if len(req) != 2 {
		t.Errorf("expected 2 required, got %v", req)
	}
}

func TestSchemaPatch_RequiredOnPropBuilder(t *testing.T) {
	type In struct {
		URL string `json:"url,omitempty"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("url").Required()
		}),
	)
	var schema map[string]any
	if err := json.Unmarshal(asRaw(r.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "url" {
		t.Errorf("required = %v", schema["required"])
	}
}

func TestSchemaPatch_RequireIdempotent(t *testing.T) {
	type In struct {
		URL string `json:"url"`
	}
	// "url" already required from omitempty-absent reflection. Calling
	// Require("url") again must not double-insert.
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Require("url")
			s.Require("url")
		}),
	)
	var schema map[string]any
	if err := json.Unmarshal(asRaw(r.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 {
		t.Errorf("required should have 1 entry after idempotent Require, got %v", req)
	}
}

func TestSchemaPatch_PropOnUnknownField_Adds(t *testing.T) {
	type In struct {
		Known string `json:"known"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("unknown").Type("string").Desc("added by patch")
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "unknown")
	if got["type"] != "string" || got["description"] != "added by patch" {
		t.Errorf("unknown prop not added correctly: %v", got)
	}
}

func TestSchemaPatch_Replace(t *testing.T) {
	type In struct {
		Payload string `json:"payload"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("payload").Replace(map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "null"},
				},
			})
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "payload")
	// After Replace, the original "type":"string" must be gone.
	if _, hasType := got["type"]; hasType {
		t.Errorf("Replace did not clear original type, got: %v", got)
	}
	if _, ok := got["anyOf"]; !ok {
		t.Errorf("Replace did not install anyOf: %v", got)
	}
}

func TestSchemaPatch_OverrideWinsOverPatch(t *testing.T) {
	type In struct {
		URL string `json:"url"`
	}
	overrideSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "from override"},
		},
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaOverride(overrideSchema),
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("url").Desc("from patch")
		}),
	)
	// Override is used verbatim; Patch's edit must not appear.
	got := unmarshalProp(t, r.InputSchema, "url")
	if got["description"] != "from override" {
		t.Errorf("Override should have won; got description=%v", got["description"])
	}
}

func TestSchemaPatch_OutputSchemaPatched(t *testing.T) {
	type Out struct {
		Total float64 `json:"total"`
	}
	r := TypedTool[struct{}, Out]("t", "desc",
		func(ctx ToolContext, _ struct{}) (Out, error) { return Out{}, nil },
		WithOutputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("total").Desc("total count").Min(0)
		}),
	)
	got := unmarshalProp(t, r.OutputSchema, "total")
	if got["description"] != "total count" {
		t.Errorf("description = %v", got["description"])
	}
	if got["minimum"].(float64) != 0 {
		t.Errorf("minimum = %v", got["minimum"])
	}
}

func TestSchemaPatch_OutputSchemaSkippedForStringOut(t *testing.T) {
	// Out=string means TypedTool emits NO outputSchema. The patch should
	// be silently skipped — no panic, no output schema field, no error.
	r := TypedTool[struct{}, string]("t", "desc",
		func(ctx ToolContext, _ struct{}) (string, error) { return "ok", nil },
		WithOutputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("anything").Desc("won't appear")
		}),
	)
	if r.OutputSchema != nil {
		t.Errorf("OutputSchema should be nil for Out=string, got: %v", r.OutputSchema)
	}
}

func TestSchemaPatch_Enum(t *testing.T) {
	type In struct {
		Mode string `json:"mode"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("mode").Enum("auto", "manual", "off")
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "mode")
	enum, ok := got["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Fatalf("enum missing or wrong shape: %v", got["enum"])
	}
}

func TestSchemaPatch_PatternAndStringLen(t *testing.T) {
	type In struct {
		Email string `json:"email"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			s.Prop("email").Pattern(`^.+@.+$`).MinLength(3).MaxLength(254)
		}),
	)
	got := unmarshalProp(t, r.InputSchema, "email")
	if got["pattern"] != `^.+@.+$` {
		t.Errorf("pattern = %v", got["pattern"])
	}
	if int(got["minLength"].(float64)) != 3 {
		t.Errorf("minLength = %v", got["minLength"])
	}
	if int(got["maxLength"].(float64)) != 254 {
		t.Errorf("maxLength = %v", got["maxLength"])
	}
}

func TestSchemaPatch_RawEscapeHatch(t *testing.T) {
	type In struct {
		Name string `json:"name"`
	}
	r := TypedTool[In, string]("t", "desc",
		func(ctx ToolContext, _ In) (string, error) { return "ok", nil },
		WithInputSchemaPatch(func(s *SchemaBuilder) {
			// Inject a top-level if/then/else conditional via Raw.
			raw := s.Raw()
			raw["if"] = map[string]any{"properties": map[string]any{"name": map[string]any{"const": "admin"}}}
			raw["then"] = map[string]any{"required": []string{"name"}}
		}),
	)
	var schema map[string]any
	if err := json.Unmarshal(asRaw(r.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema["if"]; !ok {
		t.Errorf("raw if/then injection lost: %v", schema)
	}
}

// --- helpers ---------------------------------------------------------------

// asRaw marshals an InputSchema/OutputSchema (which TypedTool stores as
// `any`) back to JSON bytes for inspection.
func asRaw(schema any) json.RawMessage {
	if rm, ok := schema.(json.RawMessage); ok {
		return rm
	}
	b, _ := json.Marshal(schema)
	return b
}

// unmarshalProp parses the schema and returns the named property's
// schema map, or fails the test if absent.
func unmarshalProp(t *testing.T, schema any, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(asRaw(schema), &m); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %v", m)
	}
	p, ok := props[name].(map[string]any)
	if !ok {
		t.Fatalf("property %q missing: %v", name, props)
	}
	return p
}
