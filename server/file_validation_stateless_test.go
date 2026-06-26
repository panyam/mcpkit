package server

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// TestFileInputValidation_StatelessWire verifies SEP-2356 file-input
// validation runs on the SEP-2575 stateless tools/call path, in parity
// with the legacy dispatch. Regression for the gap where
// callToolForStateless skipped validation entirely, letting size/MIME
// violations reach the handler instead of returning -32602.
func TestFileInputValidation_StatelessWire(t *testing.T) {
	s, url, teardown := newStatelessTestServerWithOpts(t, stateless.ModeStateless, WithFileInputValidation())
	defer teardown()

	maxSize := 1024
	if err := s.Registry().AddTool(
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
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("handler reached"), nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	// 2 KiB image against a 1 KiB cap — must be rejected before the handler.
	uri := core.EncodeDataURI(make([]byte, 2048), "image/png", "big.png")
	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "upload_image",
			"arguments": map[string]any{"image": uri},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})

	r := decode(t, resp)
	if r.Error == nil || r.Error.Code != core.ErrCodeInvalidParams {
		t.Fatalf("expected -32602 file_too_large, got %+v", r.Error)
	}
	dataRaw, _ := json.Marshal(r.Error.Data)
	var data core.FileTooLargeData
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		t.Fatalf("unmarshal error data: %v", err)
	}
	if data.Reason != core.FileInputReasonTooLarge || data.Field != "image" {
		t.Fatalf("unexpected error data: %+v", data)
	}
}
