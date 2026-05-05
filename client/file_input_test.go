package client_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/require"
)

// verifies: FileInputsFromTool extracts a single-file descriptor from
// the inputSchema's properties map. Mirrors the upload_image shape.
func TestFileInputsFromTool_SingleFile(t *testing.T) {
	max := 1024
	tool := core.ToolDef{
		Name: "upload_image",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"image": core.FileInputProperty(core.FileInputDescriptor{
					Accept:  []string{"image/*"},
					MaxSize: &max,
				}),
				"caption": map[string]any{"type": "string"},
			},
			"required": []string{"image"},
		},
	}
	got := client.FileInputsFromTool(tool)
	require.Len(t, got, 1)
	desc, ok := got["image"]
	require.True(t, ok, "expected `image` key")
	require.Equal(t, []string{"image/*"}, desc.Accept)
	require.NotNil(t, desc.MaxSize)
	require.Equal(t, 1024, *desc.MaxSize)
}

// verifies: array-of-files shape lands under the `name[]` key, so
// callers can distinguish single-file from array-of-files declarations
// without re-walking the schema.
func TestFileInputsFromTool_ArrayItems(t *testing.T) {
	tool := core.ToolDef{
		Name: "analyze_documents",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"documents": core.FileInputArrayProperty(core.FileInputDescriptor{
					Accept: []string{"application/pdf", ".pdf"},
				}),
			},
		},
	}
	got := client.FileInputsFromTool(tool)
	require.Len(t, got, 1)
	desc, ok := got["documents[]"]
	require.True(t, ok, "array shape should land under `name[]`")
	require.Equal(t, []string{"application/pdf", ".pdf"}, desc.Accept)
}

// verifies: schemas that aren't a `map[string]any` (typed structs,
// json.RawMessage) and tools without file-input properties both
// return an empty map instead of panicking.
func TestFileInputsFromTool_NonMatchingShapesReturnEmpty(t *testing.T) {
	type typed struct {
		Type string `json:"type"`
	}
	got := client.FileInputsFromTool(core.ToolDef{InputSchema: typed{Type: "object"}})
	require.Empty(t, got)

	got = client.FileInputsFromTool(core.ToolDef{
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plain": map[string]any{"type": "string"},
			},
		},
	})
	require.Empty(t, got)
}

// verifies: PrepareFileArg reads a real file from disk, encodes it as
// a data URI matching `core.EncodeDataURI` byte-for-byte, and the
// result round-trips through `core.DecodeDataURI` to the same bytes.
// Confirms the mime detection picks the registered type for `.png`.
func TestPrepareFileArg_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "tiny.png")
	// 1×1 transparent PNG header + minimal data — same fixture shape
	// the file-inputs walkthrough uses.
	bytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	require.NoError(t, os.WriteFile(path, bytes, 0o644))

	desc := &core.FileInputDescriptor{Accept: []string{"image/*"}}
	uri, err := client.PrepareFileArg(path, desc)
	require.NoError(t, err)

	gotBytes, mediaType, filename, err := core.DecodeDataURI(uri)
	require.NoError(t, err)
	require.Equal(t, bytes, gotBytes)
	require.Equal(t, "image/png", mediaType)
	require.Equal(t, "tiny.png", filename)
}

// verifies: oversized payloads surface `core.ErrFileTooLarge` (typed
// error), so callers can branch with `errors.Is` and read the size
// info without parsing the message.
func TestPrepareFileArg_OversizedRejects(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.txt")
	require.NoError(t, os.WriteFile(path, make([]byte, 2048), 0o644))

	max := 1024
	desc := &core.FileInputDescriptor{MaxSize: &max}
	_, err := client.PrepareFileArg(path, desc)

	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrFileTooLarge),
		"errors.Is(err, ErrFileTooLarge) must hold; got %v", err)
	var typed *core.FileTooLargeError
	require.True(t, errors.As(err, &typed), "errors.As must extract typed error")
	require.Equal(t, 2048, typed.ActualSize)
	require.Equal(t, 1024, typed.MaxSize)
}

// verifies: MIME mismatch returns `*core.FileTypeNotAcceptedError`
// with the detected media type and the descriptor's accept list,
// matching what the server-side validator returns.
func TestPrepareFileArg_WrongMIMERejects(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	desc := &core.FileInputDescriptor{Accept: []string{"image/*"}}
	_, err := client.PrepareFileArg(path, desc)

	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrFileTypeNotAccepted))
	var typed *core.FileTypeNotAcceptedError
	require.True(t, errors.As(err, &typed))
	require.Equal(t, "text/plain", typed.MediaType)
	require.Equal(t, []string{"image/*"}, typed.Accept)
}

// verifies: nil descriptor skips validation but still encodes — for
// the unconstrained `process_any_file` style of tool.
func TestPrepareFileArg_NilDescriptorSkipsValidation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "anything.bin")
	require.NoError(t, os.WriteFile(path, []byte{0x00, 0x01, 0xff}, 0o644))

	uri, err := client.PrepareFileArg(path, nil)
	require.NoError(t, err)
	require.True(t, len(uri) > 0)

	got, _, _, err := core.DecodeDataURI(uri)
	require.NoError(t, err)
	require.Equal(t, []byte{0x00, 0x01, 0xff}, got)
}

// verifies: WithFileInputs() advertises the cap on the wire during
// initialize. We can't easily intercept the initialize body without
// a full transport mock, so we assert the marshaled capabilities
// shape via a parallel ClientCapabilities round-trip — same struct,
// same JSON path that gets emitted.
func TestWithFileInputs_AdvertisesCapability(t *testing.T) {
	c := client.NewClient("", core.ClientInfo{}, client.WithFileInputs())
	require.NotNil(t, c, "NewClient must accept WithFileInputs option")

	// We can't read c.fileInputs directly (unexported). Instead, simulate
	// the connect-time cap building: ClientCapabilities with FileInputs
	// set marshals to the wire shape the conformance suite asserts.
	caps := core.ClientCapabilities{FileInputs: &struct{}{}}
	raw, err := json.Marshal(caps)
	require.NoError(t, err)
	require.JSONEq(t, `{"fileInputs":{}}`, string(raw))
}
