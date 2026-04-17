package mcpv1

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/panyam/protokit/wire"
)

// Extension field numbers from annotations.proto.
const (
	FieldMCPTool     protowire.Number = 51001
	FieldMCPResource protowire.Number = 51002
	FieldMCPPrompt   protowire.Number = 51003
	FieldMCPElicit   protowire.Number = 51004
	FieldMCPSampling protowire.Number = 51005
	FieldMCPService  protowire.Number = 51010
)

// GetToolOptions extracts MCPToolOptions from method options.
func GetToolOptions(opts protoreflect.ProtoMessage) *MCPToolOptions {
	raw := wire.ExtractExtension(opts, FieldMCPTool)
	if raw == nil {
		return nil
	}
	return &MCPToolOptions{
		Name:             wire.DecodeString(raw, 1),
		Description:      wire.DecodeString(raw, 2),
		Timeout:          wire.DecodeString(raw, 3),
		StructuredOutput: wire.DecodeBool(raw, 4),
		ResultSummary:    wire.DecodeString(raw, 5),
	}
}

// GetResourceOptions extracts MCPResourceOptions from method options.
func GetResourceOptions(opts protoreflect.ProtoMessage) *MCPResourceOptions {
	raw := wire.ExtractExtension(opts, FieldMCPResource)
	if raw == nil {
		return nil
	}
	return &MCPResourceOptions{
		UriTemplate:       wire.DecodeString(raw, 1),
		Name:              wire.DecodeString(raw, 2),
		MimeType:          wire.DecodeString(raw, 3),
		Description:       wire.DecodeString(raw, 4),
		CompletableFields: wire.DecodeStringList(raw, 5),
	}
}

// GetPromptOptions extracts MCPPromptOptions from method options.
func GetPromptOptions(opts protoreflect.ProtoMessage) *MCPPromptOptions {
	raw := wire.ExtractExtension(opts, FieldMCPPrompt)
	if raw == nil {
		return nil
	}
	return &MCPPromptOptions{
		Name:              wire.DecodeString(raw, 1),
		Description:       wire.DecodeString(raw, 2),
		CompletableFields: wire.DecodeStringList(raw, 3),
	}
}

// GetElicitOptions extracts MCPElicitOptions from method options.
func GetElicitOptions(opts protoreflect.ProtoMessage) *MCPElicitOptions {
	raw := wire.ExtractExtension(opts, FieldMCPElicit)
	if raw == nil {
		return nil
	}
	return &MCPElicitOptions{
		Message:       wire.DecodeString(raw, 1),
		SchemaMessage: wire.DecodeString(raw, 2),
	}
}

// GetSamplingOptions extracts MCPSamplingOptions from method options.
func GetSamplingOptions(opts protoreflect.ProtoMessage) *MCPSamplingOptions {
	raw := wire.ExtractExtension(opts, FieldMCPSampling)
	if raw == nil {
		return nil
	}
	return &MCPSamplingOptions{
		SystemPrompt:         wire.DecodeString(raw, 1),
		MaxTokens:            wire.DecodeInt32(raw, 2),
		IncludeContext:       wire.DecodeString(raw, 3),
		IntelligencePriority: wire.DecodeFloat(raw, 4),
		SpeedPriority:        wire.DecodeFloat(raw, 5),
		CostPriority:         wire.DecodeFloat(raw, 6),
	}
}

// GetServiceOptions extracts MCPServiceOptions from service options.
func GetServiceOptions(opts protoreflect.ProtoMessage) *MCPServiceOptions {
	raw := wire.ExtractExtension(opts, FieldMCPService)
	if raw == nil {
		return nil
	}
	result := &MCPServiceOptions{
		Namespace: wire.DecodeString(raw, 1),
	}
	if nested := wire.DecodeBytes(raw, 2); nested != nil {
		result.DefaultSampling = decodeSamplingOptions(nested)
	}
	if nested := wire.DecodeBytes(raw, 3); nested != nil {
		result.DefaultElicit = decodeElicitOptions(nested)
	}
	return result
}

func decodeSamplingOptions(raw []byte) *MCPSamplingOptions {
	return &MCPSamplingOptions{
		SystemPrompt:         wire.DecodeString(raw, 1),
		MaxTokens:            wire.DecodeInt32(raw, 2),
		IncludeContext:       wire.DecodeString(raw, 3),
		IntelligencePriority: wire.DecodeFloat(raw, 4),
		SpeedPriority:        wire.DecodeFloat(raw, 5),
		CostPriority:         wire.DecodeFloat(raw, 6),
	}
}

func decodeElicitOptions(raw []byte) *MCPElicitOptions {
	return &MCPElicitOptions{
		Message:       wire.DecodeString(raw, 1),
		SchemaMessage: wire.DecodeString(raw, 2),
	}
}
