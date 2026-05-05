package client

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/core"
)

// SEP-2356 Phase 1.6 — client-side conveniences for file-input tools.
//
// FileInputsFromTool extracts the per-property descriptors a server
// advertised on a tool's inputSchema. Useful when a host wants to
// pre-fill a UI based on declared constraints (accept patterns, max
// size) before invoking the tool.
//
// PrepareFileArg reads a file from disk, validates it against the
// descriptor with the same rules `core.ValidateFileInput` applies on the
// server side, and returns an RFC 2397 base64 data URI ready to drop
// into a tools/call argument map. Callers no longer need to hand-roll
// `os.ReadFile` + `core.EncodeDataURI` + manual MIME guessing — the
// helper handles all three.
//
// WithFileInputs is the client option that advertises the
// `capabilities.fileInputs` capability during initialize. Servers
// running with `server.WithFileInputValidation()` use this declaration
// to decide whether to emit `x-mcp-file` keywords in tools/list (they
// strip them for clients without the cap, per spec).

// WithFileInputs advertises SEP-2356 file-input support during the
// initialize handshake. Servers MUST NOT include `x-mcp-file` keywords
// in tool inputSchemas (or elicitation requestedSchemas) for clients
// that omit this capability — opt in here to receive the picker hints.
func WithFileInputs() ClientOption {
	return func(c *Client) { c.fileInputs = true }
}

// FileInputsFromTool returns a map of `propertyPath -> descriptor` for
// every file-input property in the tool's inputSchema. Handles both the
// single-file shape (`properties.<name>.x-mcp-file`) and the array shape
// (`properties.<name>.items.x-mcp-file`).
//
// Property paths use bracket notation for arrays so callers can
// distinguish the two shapes:
//
//	upload_image      → {"image": <desc>}
//	analyze_documents → {"documents[]": <desc>}
//
// Returns an empty map if no file-input properties are declared (or the
// schema isn't a `map[string]any`-shaped object).
func FileInputsFromTool(tool core.ToolDef) map[string]core.FileInputDescriptor {
	out := map[string]core.FileInputDescriptor{}
	schemaMap, ok := tool.InputSchema.(map[string]any)
	if !ok {
		return out
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		return out
	}
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if desc := core.ExtractFileInputDescriptor(prop); desc != nil {
			out[name] = *desc
			continue
		}
		// Array of files: items carries the descriptor.
		if items, ok := prop["items"].(map[string]any); ok {
			if desc := core.ExtractFileInputDescriptor(items); desc != nil {
				out[name+"[]"] = *desc
			}
		}
	}
	return out
}

// PrepareFileArg reads a file from disk, validates it against the
// descriptor (size + MIME / extension), and returns an RFC 2397 base64
// data URI suitable for use as a tool argument.
//
// MIME detection follows two rules in order:
//   1. mime.TypeByExtension on the filename — fast and deterministic.
//   2. Fallback to http.DetectContentType on the first 512 bytes — used
//      when the extension is unknown.
// The detected media type is what the validator checks against
// `descriptor.Accept`; the SAME type is embedded in the resulting data
// URI so the server's decoder reads back what we sent.
//
// A nil descriptor skips validation but still encodes — useful for the
// unconstrained `process_any_file`-style tool where any file is allowed.
//
// Returns the validator's typed error (`*core.FileTooLargeError`,
// `*core.FileTypeNotAcceptedError`) on failure so callers can branch
// with `errors.As` and render rich error UX.
func PrepareFileArg(path string, desc *core.FileInputDescriptor) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	mediaType := detectMediaType(path, data)
	uri := core.EncodeDataURI(data, mediaType, filepath.Base(path))
	if err := core.ValidateFileInput(uri, desc); err != nil {
		// Wrap with the file path so error messages are debuggable.
		return "", fmt.Errorf("file %s: %w", path, err)
	}
	return uri, nil
}

// detectMediaType picks a media type for an on-disk file. Prefers the
// filename's extension via mime.TypeByExtension; falls back to
// http.DetectContentType (signature sniffing) when the extension isn't
// registered. Returns "application/octet-stream" if both fail —
// matches what the spec assumes for unknown payloads.
func detectMediaType(path string, data []byte) string {
	if mt := mimeTypeByExtension(path); mt != "" {
		return mt
	}
	if len(data) > 0 {
		// http.DetectContentType samples up to 512 bytes; always returns
		// at least "application/octet-stream", never an empty string.
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

// mimeTypeByExtension is a tiny map of common types so PrepareFileArg
// stays self-contained without pulling in a full MIME database. We
// could call mime.TypeByExtension from stdlib, but that returns
// "image/jpeg; charset=binary" with extra parameters in some Go
// versions — we want the bare type for clean accept-pattern matching
// against descriptors that say `image/jpeg`. Add entries as new
// extensions surface in real usage.
func mimeTypeByExtension(path string) string {
	switch ext := filepath.Ext(path); ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".csv":
		return "text/csv"
	}
	return ""
}

// Pulled into a sentinel so tests can assert the exact unwrap chain
// from `errors.As` after wrapping. Not exported — the public errors
// (`core.ErrFileTooLarge` / `core.ErrFileTypeNotAccepted`) remain the
// caller-facing identifiers.
var _ = errors.As
