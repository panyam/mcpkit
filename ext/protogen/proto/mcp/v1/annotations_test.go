package mcpv1

import (
	"testing"

	"github.com/panyam/protokit/wire"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/encoding/protowire"
)

// buildEmbeddedMessage constructs raw proto bytes for a message with string and bool fields.
func buildEmbeddedMessage(fields map[protowire.Number]any) []byte {
	var buf []byte
	for num, val := range fields {
		switch v := val.(type) {
		case string:
			buf = protowire.AppendTag(buf, num, protowire.BytesType)
			buf = protowire.AppendString(buf, v)
		case bool:
			buf = protowire.AppendTag(buf, num, protowire.VarintType)
			if v {
				buf = protowire.AppendVarint(buf, 1)
			} else {
				buf = protowire.AppendVarint(buf, 0)
			}
		}
	}
	return buf
}

// buildOptionsBytes wraps an embedded message as an extension field in a parent message.
func buildOptionsBytes(extensionField protowire.Number, inner []byte) []byte {
	var buf []byte
	buf = protowire.AppendTag(buf, extensionField, protowire.BytesType)
	buf = protowire.AppendBytes(buf, inner)
	return buf
}

func TestDecodeString(t *testing.T) {
	tests := []struct {
		name     string
		fields   map[protowire.Number]any
		fieldNum protowire.Number
		want     string
	}{
		{
			name:     "present field",
			fields:   map[protowire.Number]any{1: "hello"},
			fieldNum: 1,
			want:     "hello",
		},
		{
			name:     "missing field",
			fields:   map[protowire.Number]any{1: "hello"},
			fieldNum: 2,
			want:     "",
		},
		{
			name:     "empty bytes",
			fields:   map[protowire.Number]any{},
			fieldNum: 1,
			want:     "",
		},
		{
			name:     "multiple fields selects correct one",
			fields:   map[protowire.Number]any{1: "first", 2: "second", 3: "third"},
			fieldNum: 2,
			want:     "second",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := buildEmbeddedMessage(tt.fields)
			got := wire.DecodeString(raw, tt.fieldNum)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeBool(t *testing.T) {
	tests := []struct {
		name     string
		fields   map[protowire.Number]any
		fieldNum protowire.Number
		want     bool
	}{
		{
			name:     "true",
			fields:   map[protowire.Number]any{4: true},
			fieldNum: 4,
			want:     true,
		},
		{
			name:     "false",
			fields:   map[protowire.Number]any{4: false},
			fieldNum: 4,
			want:     false,
		},
		{
			name:     "missing field returns false",
			fields:   map[protowire.Number]any{4: true},
			fieldNum: 5,
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := buildEmbeddedMessage(tt.fields)
			got := wire.DecodeBool(raw, tt.fieldNum)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractExtension(t *testing.T) {
	inner := buildEmbeddedMessage(map[protowire.Number]any{1: "test_tool"})

	t.Run("found", func(t *testing.T) {
		got := wire.DecodeString(inner, 1)
		assert.Equal(t, "test_tool", got)
	})

	t.Run("not found", func(t *testing.T) {
		raw := buildOptionsBytes(FieldMCPTool, inner)
		got := wire.DecodeString(raw, 99999)
		assert.Equal(t, "", got)
	})
}

func TestGetToolOptions_fromRawBytes(t *testing.T) {
	inner := buildEmbeddedMessage(map[protowire.Number]any{
		1: "my_tool",
		2: "Does something useful",
		3: "30s",
		4: true,
	})

	opts := &MCPToolOptions{
		Name:             wire.DecodeString(inner, 1),
		Description:      wire.DecodeString(inner, 2),
		Timeout:          wire.DecodeString(inner, 3),
		StructuredOutput: wire.DecodeBool(inner, 4),
	}

	assert.Equal(t, "my_tool", opts.Name)
	assert.Equal(t, "Does something useful", opts.Description)
	assert.Equal(t, "30s", opts.Timeout)
	assert.True(t, opts.StructuredOutput)
}

func TestGetResourceOptions_fromRawBytes(t *testing.T) {
	inner := buildEmbeddedMessage(map[protowire.Number]any{
		1: "user://{user_id}/profile",
		2: "User Profile",
		3: "application/json",
		4: "A user's profile data",
	})

	opts := &MCPResourceOptions{
		UriTemplate: wire.DecodeString(inner, 1),
		Name:        wire.DecodeString(inner, 2),
		MimeType:    wire.DecodeString(inner, 3),
		Description: wire.DecodeString(inner, 4),
	}

	assert.Equal(t, "user://{user_id}/profile", opts.UriTemplate)
	assert.Equal(t, "User Profile", opts.Name)
	assert.Equal(t, "application/json", opts.MimeType)
	assert.Equal(t, "A user's profile data", opts.Description)
}

func TestGetPromptOptions_fromRawBytes(t *testing.T) {
	inner := buildEmbeddedMessage(map[protowire.Number]any{
		1: "summarize",
		2: "Summarize a document",
	})

	opts := &MCPPromptOptions{
		Name:        wire.DecodeString(inner, 1),
		Description: wire.DecodeString(inner, 2),
	}

	assert.Equal(t, "summarize", opts.Name)
	assert.Equal(t, "Summarize a document", opts.Description)
}

func TestGetServiceOptions_fromRawBytes(t *testing.T) {
	inner := buildEmbeddedMessage(map[protowire.Number]any{
		1: "users",
	})

	opts := &MCPServiceOptions{
		Namespace: wire.DecodeString(inner, 1),
	}

	assert.Equal(t, "users", opts.Namespace)
}

func TestNilInputs(t *testing.T) {
	assert.Nil(t, GetToolOptions(nil))
	assert.Nil(t, GetResourceOptions(nil))
	assert.Nil(t, GetPromptOptions(nil))
	assert.Nil(t, GetElicitOptions(nil))
	assert.Nil(t, GetSamplingOptions(nil))
	assert.Nil(t, GetServiceOptions(nil))
}
