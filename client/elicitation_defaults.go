package client

import "encoding/json"

// SEP-1034 elicitation defaults.
//
// When a server sends an elicitation/create request with a RequestedSchema
// whose properties declare `default` values, the client MUST fill in those
// defaults for keys the user omitted (when the user accepted the
// elicitation). Spec: https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1034
//
// The merge happens inside mcpkit's elicitation/create dispatch path so
// user-supplied ElicitationHandlers don't need to be SEP-1034-aware. The
// handler returns whatever the user produced; mcpkit fills missing
// defaults before forwarding the response to the server.

// extractElicitationDefaults walks a RequestedSchema's `properties` and
// returns a map of property-name → default value for entries that declare
// one. Defensive type-checking: if the schema declares a `type` and the
// default doesn't match, the default is skipped — better to omit a
// malformed default than inject wire-invalid data. Returns an empty map
// for nil/invalid schemas or schemas with no defaults.
//
// Primitive types per SEP-1034: string, integer, number, boolean, plus
// string with enum. The implementation accepts any JSON-typed default
// whose Go-decoded shape is compatible with the declared schema type.
func extractElicitationDefaults(rawSchema json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(rawSchema) == 0 {
		return out
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return out
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return out
	}
	for propName, raw := range props {
		propMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		def, present := propMap["default"]
		if !present {
			continue
		}
		propType, _ := propMap["type"].(string)
		if !defaultMatchesType(def, propType) {
			continue
		}
		out[propName] = def
	}
	return out
}

// defaultMatchesType reports whether a JSON-decoded default value's Go
// shape is compatible with the declared JSON Schema primitive type. JSON
// numbers all decode as float64 in Go; `integer` schemas accept any
// float64 that's a whole number. Unknown schema types accept any value
// (server is responsible for further validation).
func defaultMatchesType(def any, schemaType string) bool {
	switch schemaType {
	case "string":
		_, ok := def.(string)
		return ok
	case "boolean":
		_, ok := def.(bool)
		return ok
	case "integer":
		f, ok := def.(float64)
		return ok && f == float64(int64(f))
	case "number":
		_, ok := def.(float64)
		return ok
	default:
		return true
	}
}

// mergeElicitationDefaults adds entries from defaults to content for any
// key not already set. Returns the (possibly-modified) content map. If
// content is nil and defaults are non-empty, a new map is allocated.
func mergeElicitationDefaults(content, defaults map[string]any) map[string]any {
	if len(defaults) == 0 {
		return content
	}
	if content == nil {
		content = map[string]any{}
	}
	for k, v := range defaults {
		if _, set := content[k]; set {
			continue
		}
		content[k] = v
	}
	return content
}
