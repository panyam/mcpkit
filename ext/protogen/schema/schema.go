// Package schema converts protobuf message descriptors into JSON Schema (2020-12)
// representations suitable for MCP tool InputSchema and OutputSchema fields.
package schema

import (
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FromMessage converts a protobuf message descriptor into a JSON Schema map.
// The returned map is suitable for use as InputSchema or OutputSchema in MCP tool definitions.
func FromMessage(md protoreflect.MessageDescriptor) map[string]any {
	return messageSchema(md, map[protoreflect.FullName]bool{})
}

func messageSchema(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	// Cycle detection: if we've already visited this message, return a ref-like stub.
	if seen[md.FullName()] {
		return map[string]any{"type": "object", "description": "recursive reference to " + string(md.FullName())}
	}
	seen[md.FullName()] = true
	defer delete(seen, md.FullName())
	properties := map[string]any{}
	var required []string
	var anyOfGroups []map[string]any

	// Track which oneof groups we've already processed.
	seenOneofs := map[string]bool{}

	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		name := string(fd.Name())

		// Handle oneof groups.
		if oneof := fd.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			oneofName := string(oneof.Name())
			if !seenOneofs[oneofName] {
				seenOneofs[oneofName] = true
				anyOfGroups = append(anyOfGroups, buildOneofSchema(oneof, seen))
			}
			continue
		}

		properties[name] = fieldSchema(fd, seen)
		if isRequired(fd) {
			required = append(required, name)
		}
	}

	result := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		result["required"] = required
	}
	if len(anyOfGroups) > 0 {
		result["anyOf"] = anyOfGroups
	}
	return result
}

// buildOneofSchema creates a JSON Schema anyOf entry for a protobuf oneof group.
func buildOneofSchema(oneof protoreflect.OneofDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	var alternatives []map[string]any
	for i := 0; i < oneof.Fields().Len(); i++ {
		fd := oneof.Fields().Get(i)
		name := string(fd.Name())
		alternatives = append(alternatives, map[string]any{
			"properties": map[string]any{name: fieldSchema(fd, seen)},
			"required":   []string{name},
		})
	}
	return map[string]any{"oneOf": alternatives}
}

// fieldSchema returns the JSON Schema for a single proto field descriptor.
func fieldSchema(fd protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	if fd.IsMap() {
		return mapSchema(fd, seen)
	}

	schema := scalarOrMessageSchema(fd, seen)

	if fd.IsList() {
		return map[string]any{
			"type":  "array",
			"items": schema,
		}
	}
	return schema
}

// mapSchema handles proto map<K,V> fields.
func mapSchema(fd protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	keyFd := fd.MapKey()
	valFd := fd.MapValue()

	result := map[string]any{
		"type":                 "object",
		"additionalProperties": scalarOrMessageSchema(valFd, seen),
	}

	// Add key type constraints for non-string keys.
	if keyConstraints := keyTypeConstraints(keyFd); keyConstraints != nil {
		result["propertyNames"] = keyConstraints
	}
	return result
}

// keyTypeConstraints returns JSON Schema constraints for map key types.
// Proto maps only allow integral types, strings, and bools as keys.
func keyTypeConstraints(fd protoreflect.FieldDescriptor) map[string]any {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{
			"type": "string",
			"enum": []string{"true", "false"},
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return map[string]any{
			"type":    "string",
			"pattern": `^-?(0|[1-9]\d*)$`,
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{
			"type":    "string",
			"pattern": `^(0|[1-9]\d*)$`,
		}
	default:
		return nil // string keys need no extra constraints
	}
}

// scalarOrMessageSchema returns the JSON Schema for a field's value type,
// without considering list/map wrappers.
func scalarOrMessageSchema(fd protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return messageKindSchema(fd, seen)
	case protoreflect.EnumKind:
		return enumSchema(fd)
	default:
		return scalarSchema(fd.Kind())
	}
}

// scalarSchema maps proto scalar kinds to JSON Schema types.
func scalarSchema(kind protoreflect.Kind) map[string]any {
	switch kind {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// 64-bit integers are encoded as strings in JSON to avoid precision loss.
		return map[string]any{"type": "string", "format": "int64"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "string"}
	}
}

// enumSchema converts a proto enum to a JSON Schema string with enum values.
func enumSchema(fd protoreflect.FieldDescriptor) map[string]any {
	var values []string
	enumDesc := fd.Enum()
	for i := 0; i < enumDesc.Values().Len(); i++ {
		values = append(values, string(enumDesc.Values().Get(i).Name()))
	}
	return map[string]any{
		"type": "string",
		"enum": values,
	}
}

// messageKindSchema handles proto message types, with special cases for well-known types.
func messageKindSchema(fd protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	fullName := string(fd.Message().FullName())

	switch fullName {
	// Temporal types
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "pattern": `^-?[0-9]+(\.[0-9]+)?s$`}

	// Dynamic types
	case "google.protobuf.Struct":
		return map[string]any{"type": "object", "additionalProperties": true}
	case "google.protobuf.Value":
		return map[string]any{"description": "Any JSON value"}
	case "google.protobuf.ListValue":
		return map[string]any{"type": "array", "items": map[string]any{}}
	case "google.protobuf.Any":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"@type": map[string]any{"type": "string"},
			},
			"required": []string{"@type"},
		}
	case "google.protobuf.FieldMask":
		return map[string]any{"type": "string"}

	// Wrapper types — nullable scalars
	case "google.protobuf.DoubleValue", "google.protobuf.FloatValue":
		return map[string]any{"type": []string{"number", "null"}}
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		return map[string]any{"type": []string{"integer", "null"}}
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return map[string]any{"type": []string{"string", "null"}, "format": "int64"}
	case "google.protobuf.StringValue":
		return map[string]any{"type": []string{"string", "null"}}
	case "google.protobuf.BoolValue":
		return map[string]any{"type": []string{"boolean", "null"}}
	case "google.protobuf.BytesValue":
		return map[string]any{"type": []string{"string", "null"}, "contentEncoding": "base64"}

	// Regular nested message — recurse.
	default:
		return messageSchema(fd.Message(), seen)
	}
}

// isRequired determines if a field should be marked required in JSON Schema.
// Proto3 fields are required unless they are optional, repeated, map, or message-typed.
func isRequired(fd protoreflect.FieldDescriptor) bool {
	if fd.HasOptionalKeyword() {
		return false
	}
	if fd.IsList() || fd.IsMap() {
		return false
	}
	// Message-typed fields are inherently optional in proto3.
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		return false
	}
	return true
}
