package main

import (
	"reflect"
	"testing"
)

func TestSynthArgs(t *testing.T) {
	cases := []struct {
		name   string
		schema any
		want   map[string]any
	}{
		{
			name: "required number and string",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number"},
					"b": map[string]any{"type": "string"},
				},
				"required": []any{"a", "b"},
			},
			want: map[string]any{"a": 1, "b": "test"},
		},
		{
			name: "optional properties omitted",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number"},
					"b": map[string]any{"type": "string"},
				},
				"required": []any{"a"},
			},
			want: map[string]any{"a": 1},
		},
		{
			name: "const default and enum win over type",
			schema: map[string]any{
				"properties": map[string]any{
					"c": map[string]any{"type": "string", "const": "fixed"},
					"d": map[string]any{"type": "number", "default": 42},
					"e": map[string]any{"type": "string", "enum": []any{"x", "y"}},
				},
				"required": []any{"c", "d", "e"},
			},
			want: map[string]any{"c": "fixed", "d": 42, "e": "x"},
		},
		{
			name: "boolean array object null",
			schema: map[string]any{
				"properties": map[string]any{
					"f": map[string]any{"type": "boolean"},
					"g": map[string]any{"type": "array"},
					"h": map[string]any{
						"type":       "object",
						"properties": map[string]any{"inner": map[string]any{"type": "integer"}},
						"required":   []any{"inner"},
					},
					"i": map[string]any{"type": "null"},
				},
				"required": []any{"f", "g", "h", "i"},
			},
			want: map[string]any{
				"f": true,
				"g": []any{},
				"h": map[string]any{"inner": 1},
				"i": nil,
			},
		},
		{
			name: "type union takes first entry",
			schema: map[string]any{
				"properties": map[string]any{
					"j": map[string]any{"type": []any{"integer", "null"}},
				},
				"required": []any{"j"},
			},
			want: map[string]any{"j": 1},
		},
		{
			name: "required without property definition",
			schema: map[string]any{
				"required": []any{"mystery"},
			},
			want: map[string]any{"mystery": "test"},
		},
		{
			name:   "non-map schema",
			schema: "not a schema",
			want:   map[string]any{},
		},
		{
			name:   "nil schema",
			schema: nil,
			want:   map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := synthArgs(tc.schema)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("synthArgs() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
