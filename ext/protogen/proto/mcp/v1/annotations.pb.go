// Code generated from annotations.proto. DO NOT EDIT.
// To regenerate: protoc --go_out=. --go_opt=paths=source_relative proto/mcp/v1/annotations.proto

package mcpv1

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// MCPToolOptions configures how a gRPC method is exposed as an MCP tool.
type MCPToolOptions struct {
	Name             string `json:"name,omitempty"`
	Description      string `json:"description,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
	StructuredOutput bool   `json:"structured_output,omitempty"`
	ResultSummary    string `json:"result_summary,omitempty"`
}

// MCPResourceOptions exposes a gRPC method as an MCP resource or resource template.
type MCPResourceOptions struct {
	URITemplate string `json:"uri_template,omitempty"`
	Name        string `json:"name,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	Description string `json:"description,omitempty"`
}

// MCPPromptOptions exposes a gRPC method as an MCP prompt.
type MCPPromptOptions struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// MCPServiceOptions configures service-level MCP settings.
type MCPServiceOptions struct {
	Namespace string `json:"namespace,omitempty"`
}

// Extension field numbers matching annotations.proto.
const (
	FieldMCPTool     = 51001
	FieldMCPResource = 51002
	FieldMCPPrompt   = 51003
	FieldMCPService  = 51010
)

// extractExtension marshals a proto message's options and scans for an extension
// field by number. Returns the raw embedded message bytes, or nil if not found.
func extractExtension(opts proto.Message, fieldNum protowire.Number) []byte {
	if opts == nil {
		return nil
	}
	raw, err := proto.Marshal(opts)
	if err != nil {
		return nil
	}
	for len(raw) > 0 {
		num, wtype, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return nil
		}
		raw = raw[n:]
		switch wtype {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(raw)
		case protowire.Fixed32Type:
			_, n = protowire.ConsumeFixed32(raw)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(raw)
		case protowire.BytesType:
			var val []byte
			val, n = protowire.ConsumeBytes(raw)
			if n >= 0 && num == fieldNum {
				return val
			}
		default:
			return nil
		}
		if n < 0 {
			return nil
		}
		raw = raw[n:]
	}
	return nil
}

// decodeString reads a string field from raw proto bytes by field number.
func decodeString(raw []byte, fieldNum protowire.Number) string {
	for len(raw) > 0 {
		num, wtype, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return ""
		}
		raw = raw[n:]
		switch wtype {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(raw)
		case protowire.Fixed32Type:
			_, n = protowire.ConsumeFixed32(raw)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(raw)
		case protowire.BytesType:
			var val []byte
			val, n = protowire.ConsumeBytes(raw)
			if n >= 0 && num == fieldNum {
				return string(val)
			}
		default:
			return ""
		}
		if n < 0 {
			return ""
		}
		raw = raw[n:]
	}
	return ""
}

// decodeBool reads a bool (varint) field from raw proto bytes by field number.
func decodeBool(raw []byte, fieldNum protowire.Number) bool {
	for len(raw) > 0 {
		num, wtype, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return false
		}
		raw = raw[n:]
		switch wtype {
		case protowire.VarintType:
			var val uint64
			val, n = protowire.ConsumeVarint(raw)
			if n >= 0 && num == fieldNum {
				return val != 0
			}
		case protowire.Fixed32Type:
			_, n = protowire.ConsumeFixed32(raw)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(raw)
		case protowire.BytesType:
			_, n = protowire.ConsumeBytes(raw)
		default:
			return false
		}
		if n < 0 {
			return false
		}
		raw = raw[n:]
	}
	return false
}

// GetToolOptions extracts MCPToolOptions from method options.
// Returns nil if the mcp_tool annotation is not present.
func GetToolOptions(opts proto.Message) *MCPToolOptions {
	raw := extractExtension(opts, FieldMCPTool)
	if raw == nil {
		return nil
	}
	return &MCPToolOptions{
		Name:             decodeString(raw, 1),
		Description:      decodeString(raw, 2),
		Timeout:          decodeString(raw, 3),
		StructuredOutput: decodeBool(raw, 4),
		ResultSummary:    decodeString(raw, 5),
	}
}

// GetResourceOptions extracts MCPResourceOptions from method options.
// Returns nil if the mcp_resource annotation is not present.
func GetResourceOptions(opts proto.Message) *MCPResourceOptions {
	raw := extractExtension(opts, FieldMCPResource)
	if raw == nil {
		return nil
	}
	return &MCPResourceOptions{
		URITemplate: decodeString(raw, 1),
		Name:        decodeString(raw, 2),
		MimeType:    decodeString(raw, 3),
		Description: decodeString(raw, 4),
	}
}

// GetPromptOptions extracts MCPPromptOptions from method options.
// Returns nil if the mcp_prompt annotation is not present.
func GetPromptOptions(opts proto.Message) *MCPPromptOptions {
	raw := extractExtension(opts, FieldMCPPrompt)
	if raw == nil {
		return nil
	}
	return &MCPPromptOptions{
		Name:        decodeString(raw, 1),
		Description: decodeString(raw, 2),
	}
}

// GetServiceOptions extracts MCPServiceOptions from service options.
// Returns nil if the mcp_service annotation is not present.
func GetServiceOptions(opts proto.Message) *MCPServiceOptions {
	raw := extractExtension(opts, FieldMCPService)
	if raw == nil {
		return nil
	}
	return &MCPServiceOptions{
		Namespace: decodeString(raw, 1),
	}
}
