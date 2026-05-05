package server_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

// newFileInputTestServer returns a server with WithFileInputValidation
// enabled and a single image-only tool registered. Reuses the
// newSchemaTestServer pattern from schema_validation_test.go (Dispatch
// bypass after initialize).
func newFileInputTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv := newSchemaTestServer(t, server.WithFileInputValidation())

	maxSize := 1024
	srv.RegisterTool(
		core.ToolDef{
			Name: "upload_image",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"image": core.FileInputProperty(core.FileInputDescriptor{
						Accept:  []string{"image/*"},
						MaxSize: &maxSize,
					}),
				},
				"required": []string{"image"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Should never be reached on rejection paths.
			return core.TextResult("handler reached"), nil
		},
	)
	return srv
}

// verifies: WithFileInputValidation enabled + a 2 KiB payload against a
// 1 KiB cap returns -32602 with the wire shape pinned by
// `conformance/file-inputs/scenarios.test.ts`:
// `data: {reason: "file_too_large", field, actualSize, maxSize}`.
func TestFileInputValidation_OversizedReturns32602(t *testing.T) {
	srv := newFileInputTestServer(t)

	big := make([]byte, 2048)
	uri := core.EncodeDataURI(big, "image/png", "big.png")
	resp := callTool(t, srv, "upload_image", map[string]any{"image": uri})

	require.NotNil(t, resp.Error, "expected JSON-RPC error, got result")
	require.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)

	dataRaw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var data core.FileTooLargeData
	require.NoError(t, json.Unmarshal(dataRaw, &data))
	require.Equal(t, core.FileInputReasonTooLarge, data.Reason)
	require.Equal(t, "image", data.Field)
	require.Equal(t, 2048, data.ActualSize)
	require.Equal(t, 1024, data.MaxSize)
}

// verifies: a payload whose decoded media type is outside the
// descriptor's accept list returns -32602 with reason
// `file_type_not_accepted` plus the offending mediaType + the
// declared accept list.
func TestFileInputValidation_WrongMIMEReturns32602(t *testing.T) {
	srv := newFileInputTestServer(t)

	uri := core.EncodeDataURI([]byte("hello"), "text/plain", "x.txt")
	resp := callTool(t, srv, "upload_image", map[string]any{"image": uri})

	require.NotNil(t, resp.Error)
	require.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)

	dataRaw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var data core.FileTypeNotAcceptedData
	require.NoError(t, json.Unmarshal(dataRaw, &data))
	require.Equal(t, core.FileInputReasonTypeNotAccepted, data.Reason)
	require.Equal(t, "image", data.Field)
	require.Equal(t, "text/plain", data.MediaType)
	require.Equal(t, []string{"image/*"}, data.Accept)
}

// verifies: an in-budget image payload reaches the handler. Smoke check
// that the validator isn't over-rejecting compliant inputs.
func TestFileInputValidation_ValidPasses(t *testing.T) {
	srv := newFileInputTestServer(t)

	uri := core.EncodeDataURI([]byte{0x89, 0x50, 0x4E, 0x47}, "image/png", "tiny.png")
	resp := callTool(t, srv, "upload_image", map[string]any{"image": uri})

	require.Nil(t, resp.Error, "valid input should not be rejected")
	require.NotNil(t, resp.Result)
}

// verifies: the same descriptor with an array shape (file inputs as
// `documents[]` rather than a single property) routes through the
// items-walker correctly. Catches a regression where the dispatcher
// only inspects top-level properties and skips array items.
func TestFileInputValidation_ArrayItems(t *testing.T) {
	srv := newSchemaTestServer(t, server.WithFileInputValidation())
	srv.RegisterTool(
		core.ToolDef{
			Name: "analyze_documents",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"documents": core.FileInputArrayProperty(core.FileInputDescriptor{
						Accept: []string{"application/pdf"},
					}),
				},
				"required": []string{"documents"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	good := core.EncodeDataURI([]byte("%PDF-1.4\n"), "application/pdf", "a.pdf")
	bad := core.EncodeDataURI([]byte("hello"), "text/plain", "b.txt")

	resp := callTool(t, srv, "analyze_documents", map[string]any{
		"documents": []string{good, bad},
	})
	require.NotNil(t, resp.Error, "second item should fail validation")
	require.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)

	dataRaw, _ := json.Marshal(resp.Error.Data)
	var data core.FileTypeNotAcceptedData
	require.NoError(t, json.Unmarshal(dataRaw, &data))
	require.Equal(t, "documents[1]", data.Field,
		"field path must reflect the offending array index")
}

// verifies: the option-disabled path leaves arguments untouched and
// reaches the handler even with a non-conforming payload (so consumers
// who prefer to validate themselves keep their existing behavior).
func TestFileInputValidation_DisabledByDefault(t *testing.T) {
	// No WithFileInputValidation() option here.
	srv := newSchemaTestServer(t)

	maxSize := 16
	srv.RegisterTool(
		core.ToolDef{
			Name: "upload_image",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"image": core.FileInputProperty(core.FileInputDescriptor{
						Accept:  []string{"image/*"},
						MaxSize: &maxSize,
					}),
				},
				"required": []string{"image"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("handler reached"), nil
		},
	)

	// Wrong MIME + oversized — would be rejected with validation enabled.
	uri := core.EncodeDataURI(make([]byte, 1024), "text/plain", "x.txt")
	resp := callTool(t, srv, "upload_image", map[string]any{"image": uri})

	require.Nil(t, resp.Error, "default behavior must NOT auto-validate file inputs")
	require.NotNil(t, resp.Result)
}
