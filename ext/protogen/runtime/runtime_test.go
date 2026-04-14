package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBindProto(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		// structpb.Struct is a regular proto message with a fields map.
		// protojson accepts it as a plain JSON object.
		req := core.ToolRequest{
			Arguments: json.RawMessage(`{"name": "test"}`),
		}
		msg := &structpb.Struct{}
		err := BindProto(req, msg)
		require.NoError(t, err)
		assert.Contains(t, msg.Fields, "name")
		assert.Equal(t, "test", msg.Fields["name"].GetStringValue())
	})

	t.Run("empty args", func(t *testing.T) {
		req := core.ToolRequest{}
		msg := &structpb.Struct{}
		err := BindProto(req, msg)
		require.NoError(t, err)
	})

	t.Run("null args", func(t *testing.T) {
		req := core.ToolRequest{
			Arguments: json.RawMessage(`null`),
		}
		msg := &structpb.Struct{}
		err := BindProto(req, msg)
		require.NoError(t, err)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := core.ToolRequest{
			Arguments: json.RawMessage(`{not json}`),
		}
		msg := &structpb.Struct{}
		err := BindProto(req, msg)
		assert.Error(t, err)
	})
}

func TestProtoTextResult(t *testing.T) {
	msg := structpb.NewStringValue("hello")
	result, err := ProtoTextResult(msg)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "hello")
}

func TestProtoStructuredResult(t *testing.T) {
	// Use a Struct so protojson emits a JSON object (not a scalar).
	msg, _ := structpb.NewStruct(map[string]any{"count": 42})
	result, err := ProtoStructuredResult(msg)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, result.StructuredContent)

	m, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(42), m["count"])
}

func TestProtoResourceResult(t *testing.T) {
	msg := wrapperspb.String("data")
	result, err := ProtoResourceResult(msg, "test://resource", "application/json")
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, "test://resource", result.Contents[0].URI)
	assert.Equal(t, "application/json", result.Contents[0].MimeType)
	assert.Contains(t, result.Contents[0].Text, "data")
}

func TestRPCError(t *testing.T) {
	result, err := RPCError(errors.New("permission denied"))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "permission denied", result.Content[0].Text)
}

// TestBindParams verifies that BindParams populates proto fields from a
// string map via protokit's PopulateFromMap.
func TestBindParams(t *testing.T) {
	t.Run("populates string field", func(t *testing.T) {
		msg := wrapperspb.String("")
		err := BindParams(map[string]string{"value": "hello"}, msg)
		require.NoError(t, err)
		assert.Equal(t, "hello", msg.GetValue())
	})

	t.Run("empty map is no-op", func(t *testing.T) {
		msg := wrapperspb.String("original")
		err := BindParams(map[string]string{}, msg)
		require.NoError(t, err)
		assert.Equal(t, "original", msg.GetValue())
	})
}
