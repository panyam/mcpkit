package schema

import (
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/protogen/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Helpers ---

// msg builds a single-file ProtoSet and returns the named message's schema.
func msg(t *testing.T, messages []testutil.Message, name string, enums ...testutil.Enum) map[string]any {
	t.Helper()
	plugin := testutil.CreatePlugin(t, &testutil.ProtoSet{
		Files: []testutil.File{{
			Name:     "test.proto",
			Pkg:      "test",
			Messages: messages,
			Enums:    enums,
		}},
	})
	md := testutil.FindMessage(t, plugin, name)
	return FromMessage(md)
}

// --- Scalar type tests ---

func TestScalarFields(t *testing.T) {
	tests := []struct {
		typeName string
		wantType any
		extra    map[string]any
	}{
		{"string", "string", nil},
		{"bool", "boolean", nil},
		{"int32", "integer", nil},
		{"sint32", "integer", nil},
		{"sfixed32", "integer", nil},
		{"uint32", "integer", nil},
		{"fixed32", "integer", nil},
		{"int64", "string", map[string]any{"format": "int64"}},
		{"sint64", "string", map[string]any{"format": "int64"}},
		{"sfixed64", "string", map[string]any{"format": "int64"}},
		{"uint64", "string", map[string]any{"format": "int64"}},
		{"fixed64", "string", map[string]any{"format": "int64"}},
		{"float", "number", nil},
		{"double", "number", nil},
		{"bytes", "string", map[string]any{"contentEncoding": "base64"}},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			schema := msg(t, []testutil.Message{{
				Name: "Msg",
				Fields: []testutil.Field{
					{Name: "field", Number: 1, TypeName: tt.typeName, OneofIndex: -1},
				},
			}}, "Msg")

			props := schema["properties"].(map[string]any)
			fs := props["field"].(map[string]any)
			assert.Equal(t, tt.wantType, fs["type"])
			for k, v := range tt.extra {
				assert.Equal(t, v, fs[k], "property %q", k)
			}

			// Scalar fields are required in proto3.
			req := schema["required"].([]string)
			assert.Contains(t, req, "field")
		})
	}
}

func TestOptionalFieldNotRequired(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Msg",
		Fields: []testutil.Field{
			{Name: "required_field", Number: 1, TypeName: "string", OneofIndex: -1},
			{Name: "optional_field", Number: 2, TypeName: "string", OneofIndex: -1, Optional: true},
		},
	}}, "Msg")

	req := schema["required"].([]string)
	assert.Contains(t, req, "required_field")
	assert.NotContains(t, req, "optional_field")
}

func TestRepeatedField(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Msg",
		Fields: []testutil.Field{
			{Name: "tags", Number: 1, TypeName: "string", Repeated: true, OneofIndex: -1},
		},
	}}, "Msg")

	props := schema["properties"].(map[string]any)
	fs := props["tags"].(map[string]any)
	assert.Equal(t, "array", fs["type"])
	assert.Equal(t, "string", fs["items"].(map[string]any)["type"])

	_, hasRequired := schema["required"]
	assert.False(t, hasRequired)
}

func TestNestedMessage(t *testing.T) {
	schema := msg(t, []testutil.Message{
		{
			Name: "Inner",
			Fields: []testutil.Field{
				{Name: "value", Number: 1, TypeName: "string", OneofIndex: -1},
			},
		},
		{
			Name: "Outer",
			Fields: []testutil.Field{
				{Name: "name", Number: 1, TypeName: "string", OneofIndex: -1},
				{Name: "inner", Number: 2, TypeName: "test.Inner", OneofIndex: -1},
			},
		},
	}, "Outer")

	props := schema["properties"].(map[string]any)

	// Nested message should be an object with its own properties.
	innerSchema := props["inner"].(map[string]any)
	assert.Equal(t, "object", innerSchema["type"])
	innerProps := innerSchema["properties"].(map[string]any)
	assert.Equal(t, "string", innerProps["value"].(map[string]any)["type"])

	// Message fields are not required in proto3.
	req := schema["required"].([]string)
	assert.Contains(t, req, "name")
	assert.NotContains(t, req, "inner")
}

func TestEnumField(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Msg",
		Fields: []testutil.Field{
			{Name: "status", Number: 1, EnumType: "test.Status", OneofIndex: -1},
		},
	}}, "Msg", testutil.Enum{
		Name: "Status",
		Values: []testutil.EnumValue{
			{Name: "UNKNOWN", Number: 0},
			{Name: "ACTIVE", Number: 1},
			{Name: "INACTIVE", Number: 2},
		},
	})

	props := schema["properties"].(map[string]any)
	fs := props["status"].(map[string]any)
	assert.Equal(t, "string", fs["type"])
	assert.Equal(t, []string{"UNKNOWN", "ACTIVE", "INACTIVE"}, fs["enum"])
}

// --- Map tests ---

func TestMapStringToString(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Msg",
		Fields: []testutil.Field{
			{Name: "labels", Number: 1, TypeName: "string", IsMap: true, MapKeyType: "string", OneofIndex: -1},
		},
	}}, "Msg")

	props := schema["properties"].(map[string]any)
	ms := props["labels"].(map[string]any)
	assert.Equal(t, "object", ms["type"])
	assert.Equal(t, "string", ms["additionalProperties"].(map[string]any)["type"])

	// String keys → no propertyNames constraint.
	_, hasPN := ms["propertyNames"]
	assert.False(t, hasPN)
}

func TestMapIntKeys(t *testing.T) {
	tests := []struct {
		keyType string
		pattern string
	}{
		{"int32", `^-?(0|[1-9]\d*)$`},
		{"uint32", `^(0|[1-9]\d*)$`},
		{"int64", `^-?(0|[1-9]\d*)$`},
		{"uint64", `^(0|[1-9]\d*)$`},
	}
	for _, tt := range tests {
		t.Run(tt.keyType+"_keys", func(t *testing.T) {
			schema := msg(t, []testutil.Message{{
				Name: "Msg",
				Fields: []testutil.Field{
					{Name: "counts", Number: 1, TypeName: "int32", IsMap: true, MapKeyType: tt.keyType, OneofIndex: -1},
				},
			}}, "Msg")

			props := schema["properties"].(map[string]any)
			ms := props["counts"].(map[string]any)
			pn := ms["propertyNames"].(map[string]any)
			assert.Equal(t, "string", pn["type"])
			assert.Equal(t, tt.pattern, pn["pattern"])
		})
	}
}

func TestMapBoolKeys(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Msg",
		Fields: []testutil.Field{
			{Name: "flags", Number: 1, TypeName: "string", IsMap: true, MapKeyType: "bool", OneofIndex: -1},
		},
	}}, "Msg")

	props := schema["properties"].(map[string]any)
	ms := props["flags"].(map[string]any)
	pn := ms["propertyNames"].(map[string]any)
	assert.Equal(t, []string{"true", "false"}, pn["enum"])
}

func TestMapWithMessageValues(t *testing.T) {
	schema := msg(t, []testutil.Message{
		{
			Name: "Item",
			Fields: []testutil.Field{
				{Name: "id", Number: 1, TypeName: "string", OneofIndex: -1},
			},
		},
		{
			Name: "Msg",
			Fields: []testutil.Field{
				{Name: "items", Number: 1, TypeName: "test.Item", IsMap: true, MapKeyType: "string", OneofIndex: -1},
			},
		},
	}, "Msg")

	props := schema["properties"].(map[string]any)
	ms := props["items"].(map[string]any)
	assert.Equal(t, "object", ms["type"])
	addlProps := ms["additionalProperties"].(map[string]any)
	assert.Equal(t, "object", addlProps["type"])
	assert.Equal(t, "string", addlProps["properties"].(map[string]any)["id"].(map[string]any)["type"])
}

// --- Oneof tests ---

func TestOneofField(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name:   "Msg",
		Oneofs: []string{"value"},
		Fields: []testutil.Field{
			{Name: "text", Number: 1, TypeName: "string", OneofIndex: 0},
			{Name: "number", Number: 2, TypeName: "int32", OneofIndex: 0},
		},
	}}, "Msg")

	// Oneof fields should not appear in top-level properties.
	props := schema["properties"].(map[string]any)
	assert.Empty(t, props)

	anyOf := schema["anyOf"].([]map[string]any)
	require.Len(t, anyOf, 1)
	oneOf := anyOf[0]["oneOf"].([]map[string]any)
	require.Len(t, oneOf, 2)

	alt1 := oneOf[0]["properties"].(map[string]any)
	assert.Equal(t, "string", alt1["text"].(map[string]any)["type"])
	assert.Equal(t, []string{"text"}, oneOf[0]["required"])

	alt2 := oneOf[1]["properties"].(map[string]any)
	assert.Equal(t, "integer", alt2["number"].(map[string]any)["type"])
	assert.Equal(t, []string{"number"}, oneOf[1]["required"])
}

// --- Edge cases ---

func TestEmptyMessage(t *testing.T) {
	schema := msg(t, []testutil.Message{{Name: "Empty"}}, "Empty")

	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, map[string]any{}, schema["properties"])
	_, hasRequired := schema["required"]
	assert.False(t, hasRequired)
}

func TestRepeatedMessageField(t *testing.T) {
	schema := msg(t, []testutil.Message{
		{
			Name: "Item",
			Fields: []testutil.Field{
				{Name: "id", Number: 1, TypeName: "string", OneofIndex: -1},
			},
		},
		{
			Name: "Msg",
			Fields: []testutil.Field{
				{Name: "items", Number: 1, TypeName: "test.Item", Repeated: true, OneofIndex: -1},
			},
		},
	}, "Msg")

	props := schema["properties"].(map[string]any)
	fs := props["items"].(map[string]any)
	assert.Equal(t, "array", fs["type"])
	items := fs["items"].(map[string]any)
	assert.Equal(t, "object", items["type"])
	assert.Equal(t, "string", items["properties"].(map[string]any)["id"].(map[string]any)["type"])
}

func TestMixedFieldsRequiredness(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name: "Mixed",
		Fields: []testutil.Field{
			{Name: "name", Number: 1, TypeName: "string", OneofIndex: -1},                          // required
			{Name: "tags", Number: 2, TypeName: "string", Repeated: true, OneofIndex: -1},           // not required
			{Name: "count", Number: 3, TypeName: "int32", OneofIndex: -1},                           // required
			{Name: "child", Number: 4, TypeName: "test.Mixed", OneofIndex: -1},                      // not required (message)
			{Name: "nickname", Number: 5, TypeName: "string", Optional: true, OneofIndex: -1},       // not required (optional)
		},
	}}, "Mixed")

	req := schema["required"].([]string)
	assert.Contains(t, req, "name")
	assert.Contains(t, req, "count")
	assert.NotContains(t, req, "tags")
	assert.NotContains(t, req, "child")
	assert.NotContains(t, req, "nickname")
}

func TestDeeplyNestedMessage(t *testing.T) {
	schema := msg(t, []testutil.Message{
		{
			Name: "Level2",
			Fields: []testutil.Field{
				{Name: "value", Number: 1, TypeName: "int32", OneofIndex: -1},
			},
		},
		{
			Name: "Level1",
			Fields: []testutil.Field{
				{Name: "child", Number: 1, TypeName: "test.Level2", OneofIndex: -1},
			},
		},
		{
			Name: "Root",
			Fields: []testutil.Field{
				{Name: "nested", Number: 1, TypeName: "test.Level1", OneofIndex: -1},
			},
		},
	}, "Root")

	props := schema["properties"].(map[string]any)
	l1 := props["nested"].(map[string]any)
	assert.Equal(t, "object", l1["type"])
	l1Props := l1["properties"].(map[string]any)
	l2 := l1Props["child"].(map[string]any)
	assert.Equal(t, "object", l2["type"])
	l2Props := l2["properties"].(map[string]any)
	assert.Equal(t, "integer", l2Props["value"].(map[string]any)["type"])
}

func TestMultipleOneofGroups(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name:   "Msg",
		Oneofs: []string{"identity", "content"},
		Fields: []testutil.Field{
			{Name: "email", Number: 1, TypeName: "string", OneofIndex: 0},
			{Name: "phone", Number: 2, TypeName: "string", OneofIndex: 0},
			{Name: "text", Number: 3, TypeName: "string", OneofIndex: 1},
			{Name: "binary", Number: 4, TypeName: "bytes", OneofIndex: 1},
		},
	}}, "Msg")

	anyOf := schema["anyOf"].([]map[string]any)
	require.Len(t, anyOf, 2)

	// First group: identity (email | phone)
	oneOf1 := anyOf[0]["oneOf"].([]map[string]any)
	require.Len(t, oneOf1, 2)

	// Second group: content (text | binary)
	oneOf2 := anyOf[1]["oneOf"].([]map[string]any)
	require.Len(t, oneOf2, 2)
}

func TestMixedOneofAndRegularFields(t *testing.T) {
	schema := msg(t, []testutil.Message{{
		Name:   "Msg",
		Oneofs: []string{"choice"},
		Fields: []testutil.Field{
			{Name: "id", Number: 1, TypeName: "string", OneofIndex: -1},       // regular
			{Name: "option_a", Number: 2, TypeName: "string", OneofIndex: 0},   // oneof
			{Name: "option_b", Number: 3, TypeName: "int32", OneofIndex: 0},    // oneof
		},
	}}, "Msg")

	// Regular field in properties.
	props := schema["properties"].(map[string]any)
	assert.Contains(t, props, "id")
	assert.NotContains(t, props, "option_a")
	assert.NotContains(t, props, "option_b")

	// Oneof in anyOf.
	anyOf := schema["anyOf"].([]map[string]any)
	require.Len(t, anyOf, 1)

	// Regular field is required.
	req := schema["required"].([]string)
	assert.Contains(t, req, "id")
}
