package core

import (
	"encoding/json"
	"time"
)

// ResourceDef describes a resource exposed via MCP.
type ResourceDef struct {
	// URI uniquely identifies this resource.
	URI string `json:"uri"`

	// Name is a human-readable short name.
	Name string `json:"name"`

	// Title is an optional display title.
	Title string `json:"title,omitempty"`

	// Description explains what this resource provides.
	Description string `json:"description,omitempty"`

	// MimeType is the MIME type of the resource content.
	MimeType string `json:"mimeType,omitempty"`

	// Annotations holds optional metadata for this resource.
	Annotations map[string]any `json:"annotations,omitempty"`

	// Timeout is a per-resource execution timeout. Not serialized to clients.
	Timeout time.Duration `json:"-"`
}

// ResourceTemplate describes a parameterized resource URI template.
type ResourceTemplate struct {
	// URITemplate is an RFC 6570 URI template (e.g., "file:///{path}").
	// This serves as the unique identifier for the template in the registry —
	// registering a template with the same URI template string overwrites the
	// previous registration.
	URITemplate string `json:"uriTemplate"`

	// Name is a human-readable short name.
	Name string `json:"name"`

	// Title is an optional display title.
	Title string `json:"title,omitempty"`

	// Description explains what this template provides.
	Description string `json:"description,omitempty"`

	// MimeType is the default MIME type for resources matching this template.
	MimeType string `json:"mimeType,omitempty"`

	// Annotations holds optional metadata for this template.
	Annotations map[string]any `json:"annotations,omitempty"`

	// Timeout is a per-template execution timeout. Not serialized to clients.
	Timeout time.Duration `json:"-"`
}

// ResourcesListResult is the typed result for resources/list responses.
type ResourcesListResult struct {
	Resources  []ResourceDef `json:"resources"`
	NextCursor string        `json:"nextCursor,omitempty"`

	// TTL is the SEP-2549 cache freshness hint in seconds. See
	// ToolsListResult.TTL for full semantics — same three-state pointer
	// shape (nil = no guidance, &0 = do not cache, &N>0 = N seconds fresh).
	TTL *int `json:"ttl,omitempty"`
}

// ResourceTemplatesListResult is the typed result for resources/templates/list responses.
type ResourceTemplatesListResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        string             `json:"nextCursor,omitempty"`

	// TTL is the SEP-2549 cache freshness hint in seconds. See
	// ToolsListResult.TTL for full semantics — same three-state pointer
	// shape (nil = no guidance, &0 = do not cache, &N>0 = N seconds fresh).
	TTL *int `json:"ttl,omitempty"`
}

// ResourceReadContent is a single content item returned by resources/read.
// Either Text or Blob is set, not both.
type ResourceReadContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`

	// Meta holds per-content metadata (e.g., UI overrides).
	// Takes precedence over the resource-level _meta from resources/list.
	Meta *ResourceContentMeta `json:"_meta,omitempty"`
}

// ResourceRequest is the validated input passed to a ResourceHandler.
type ResourceRequest struct {
	URI string
}

// ResourceResult is the response from a resource handler.
type ResourceResult struct {
	Contents []ResourceReadContent `json:"contents"`
}

// UnmarshalJSON decodes a ResourceResult, tolerating a single-object
// `contents` form from peers that emit a bare object instead of the
// spec-canonical array. Single objects are wrapped into a 1-element slice.
// See #81.
func (r *ResourceResult) UnmarshalJSON(data []byte) error {
	var aux struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Contents = nil
	return decodeResourceReadSlice(aux.Contents, &r.Contents)
}

// ResourceHandler reads a resource by URI.
type ResourceHandler func(ctx ResourceContext, req ResourceRequest) (ResourceResult, error)

// TemplateHandler reads a resource matched by a URI template.
// The uri parameter is the full resolved URI, params contains the extracted template variables.
type TemplateHandler func(ctx ResourceContext, uri string, params map[string]string) (ResourceResult, error)

// ResourceUpdatedNotification is the params payload for notifications/resources/updated.
// Sent by the server to subscribed clients when a resource's content has changed.
type ResourceUpdatedNotification struct {
	URI string `json:"uri"`
}
