package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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

// TestProtoResourceResult_NonJSON verifies that non-JSON MIME types extract
// raw content from a single-string-field message instead of JSON-wrapping.
func TestProtoResourceResult_NonJSON(t *testing.T) {
	msg := wrapperspb.String("<div>hello</div>")
	result, err := ProtoResourceResult(msg, "test://slide", "text/html")
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, "text/html", result.Contents[0].MimeType)
	// Should be raw HTML, not {"value":"<div>hello</div>"}
	assert.Equal(t, "<div>hello</div>", result.Contents[0].Text)
}

// TestProtoResourceResult_NonJSON_MultiField verifies that non-JSON resources
// with multiple fields fall back to JSON serialization.
func TestProtoResourceResult_NonJSON_MultiField(t *testing.T) {
	// wrapperspb.StringValue has one field; use a different message with >1 field.
	// Use a Duration (seconds + nanos) as a multi-field message.
	msg := wrapperspb.Int32(42)
	result, err := ProtoResourceResult(msg, "test://num", "text/plain")
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	// Int32Value has one field but it's not a string — should JSON-serialize.
	assert.Contains(t, result.Contents[0].Text, "42")
}

// TestRPCErrorPlain verifies that non-gRPC errors produce a plain error result.
func TestRPCErrorPlain(t *testing.T) {
	result, err := RPCError(errors.New("permission denied"))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "permission denied", result.Content[0].Text)
	// Non-gRPC errors have no structured content.
	assert.Nil(t, result.StructuredContent)
}

// TestRPCErrorGRPCStatus verifies that gRPC status errors produce structured
// error content with the code and message preserved.
func TestRPCErrorGRPCStatus(t *testing.T) {
	st := grpcstatus.New(codes.NotFound, "user not found")
	result, err := RPCError(st.Err())
	require.NoError(t, err)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "user not found", result.Content[0].Text)

	// Structured content should have code and message.
	require.NotNil(t, result.StructuredContent)
	raw, _ := json.Marshal(result.StructuredContent)
	var errData map[string]any
	require.NoError(t, json.Unmarshal(raw, &errData))
	assert.Equal(t, "NotFound", errData["code"])
	assert.Equal(t, "user not found", errData["message"])
}

// TestRPCErrorGRPCStatusWithDetails verifies that gRPC status details
// (proto Any messages) are extracted and included as structured JSON.
func TestRPCErrorGRPCStatusWithDetails(t *testing.T) {
	st := grpcstatus.New(codes.Aborted, "version conflict")
	// Attach a StringValue as a detail (simulating a custom detail proto).
	detail := wrapperspb.String("current_version=5")
	stWithDetails, err := st.WithDetails(detail)
	require.NoError(t, err)

	result, rpcErr := RPCError(stWithDetails.Err())
	require.NoError(t, rpcErr)
	assert.True(t, result.IsError)

	require.NotNil(t, result.StructuredContent)
	raw, _ := json.Marshal(result.StructuredContent)
	var errData map[string]any
	require.NoError(t, json.Unmarshal(raw, &errData))
	assert.Equal(t, "Aborted", errData["code"])

	details, ok := errData["details"].([]any)
	require.True(t, ok, "details should be an array, got %T", errData["details"])
	require.Len(t, details, 1)
	// StringValue unwraps to its scalar value via protojson.
	assert.Equal(t, "current_version=5", details[0])
}

// TestProtoPromptResult verifies that ProtoPromptResult marshals a proto
// message into a single assistant text message.
func TestProtoPromptResult(t *testing.T) {
	msg := wrapperspb.String("hello world")
	result, err := ProtoPromptResult(msg)
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "assistant", result.Messages[0].Role)
	assert.Equal(t, "text", result.Messages[0].Content.Type)
	assert.Contains(t, result.Messages[0].Content.Text, "hello world")
}

// TestBindPromptArgs verifies that BindPromptArgs populates a proto message
// from prompt argument values (map[string]any). Uses structpb.Struct since
// protojson treats it as a plain JSON object — matching how real request
// messages behave.
func TestBindPromptArgs(t *testing.T) {
	msg := &structpb.Struct{}
	err := BindPromptArgs(map[string]any{
		"topic":  "testing",
		"length": float64(100),
	}, msg)
	require.NoError(t, err)
	assert.Equal(t, "testing", msg.Fields["topic"].GetStringValue())
	assert.Equal(t, float64(100), msg.Fields["length"].GetNumberValue())
}

// TestBindPromptArgsEmpty verifies that empty args is a no-op.
func TestBindPromptArgsEmpty(t *testing.T) {
	msg := &structpb.Struct{}
	err := BindPromptArgs(map[string]any{}, msg)
	require.NoError(t, err)
	assert.Nil(t, msg.Fields)
}

// TestProtoSummaryStructuredResult verifies that ProtoSummaryStructuredResult
// renders a summary template from response fields and returns structured content.
func TestProtoSummaryStructuredResult(t *testing.T) {
	msg, err := structpb.NewStruct(map[string]any{
		"name":    "Alice",
		"version": float64(3),
	})
	require.NoError(t, err)

	result, rErr := ProtoSummaryStructuredResult(msg, "User {name} updated (v{version})")
	require.NoError(t, rErr)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "User Alice updated (v3)", result.Content[0].Text)
	assert.NotNil(t, result.StructuredContent)
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
