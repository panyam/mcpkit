package core

import "context"

// FileInputSchemaKey is the JSON Schema extension keyword used to mark a
// string property as a file input (SEP-2356). Servers attach a
// FileInputDescriptor under this key on schema properties of
// {"type": "string", "format": "uri"} to signal "render a file picker here."
//
// Clients that declare the fileInputs capability encode selected files as
// RFC 2397 data URIs and pass them as the property's string value.
const FileInputSchemaKey = "x-mcp-file"

// FileInputDescriptor is the value of the x-mcp-file schema extension
// keyword (SEP-2356). It tells the client which file types the server
// will accept and the maximum decoded size in bytes.
//
// Both fields are optional. An empty descriptor (`{}`) means "any file,
// any size" — the server still has to validate the payload at the
// transport boundary.
type FileInputDescriptor struct {
	// Accept is the list of accepted MIME patterns or file extensions.
	// Each entry is one of:
	//   - exact MIME type:    "image/png"
	//   - wildcard subtype:   "image/*"
	//   - file extension:     ".pdf"  (matched against known MIME types)
	//
	// Empty Accept means any type is allowed.
	Accept []string `json:"accept,omitempty"`

	// MaxSize is the maximum size in bytes of the decoded file payload.
	// nil means no server-declared limit (the client may still impose its own).
	MaxSize *int `json:"maxSize,omitempty"`
}

// HasFileInputs reports whether the connected client declared the SEP-2356
// fileInputs capability during the initialize handshake. Servers consult
// this before advertising x-mcp-file in tool inputSchemas — per spec, the
// keyword MUST NOT appear if the client cannot handle it.
func HasFileInputs(ctx context.Context) bool {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.clientCaps == nil {
		return false
	}
	return sc.clientCaps.FileInputs != nil
}

// FileInputProperty builds a JSON Schema property for a single file input.
// The returned map is shaped as `{"type": "string", "format": "uri",
// "x-mcp-file": desc}` and is suitable for embedding in a tool's
// inputSchema properties.
func FileInputProperty(desc FileInputDescriptor) map[string]any {
	return map[string]any{
		"type":             "string",
		"format":           "uri",
		FileInputSchemaKey: desc,
	}
}

// FileInputArrayProperty builds a JSON Schema property for an array of
// file inputs. The returned map is shaped as `{"type": "array", "items":
// {"type": "string", "format": "uri", "x-mcp-file": desc}}`.
func FileInputArrayProperty(desc FileInputDescriptor) map[string]any {
	return map[string]any{
		"type":  "array",
		"items": FileInputProperty(desc),
	}
}

// ExtractFileInputDescriptor pulls the x-mcp-file descriptor from a JSON
// Schema property, or nil if the keyword is absent.
//
// The descriptor may have been built by FileInputProperty (Go map of
// FileInputDescriptor) or unmarshalled from JSON (map[string]any with
// "accept" / "maxSize" fields) — both shapes are handled.
func ExtractFileInputDescriptor(schemaProp map[string]any) *FileInputDescriptor {
	if schemaProp == nil {
		return nil
	}
	raw, ok := schemaProp[FileInputSchemaKey]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case FileInputDescriptor:
		copy := v
		return &copy
	case *FileInputDescriptor:
		return v
	case map[string]any:
		return descriptorFromMap(v)
	}
	return nil
}

func descriptorFromMap(m map[string]any) *FileInputDescriptor {
	desc := FileInputDescriptor{}
	if accept, ok := m["accept"].([]any); ok {
		desc.Accept = make([]string, 0, len(accept))
		for _, a := range accept {
			if s, ok := a.(string); ok {
				desc.Accept = append(desc.Accept, s)
			}
		}
	} else if accept, ok := m["accept"].([]string); ok {
		desc.Accept = append([]string(nil), accept...)
	}
	switch n := m["maxSize"].(type) {
	case float64:
		v := int(n)
		desc.MaxSize = &v
	case int:
		v := n
		desc.MaxSize = &v
	case *int:
		desc.MaxSize = n
	}
	return &desc
}
