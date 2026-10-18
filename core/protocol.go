package core

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Protocol-level types shared between server and client packages.
// These are MCP spec types that appear in both directions of communication.

// Implementation describes the software on one end of an MCP connection.
// The spec uses this one type for both identity slots: serverInfo and
// clientInfo on initialize, and the io.modelcontextprotocol/serverInfo and
// io.modelcontextprotocol/clientInfo _meta keys on the 2026-07-28 wire.
// Only Name and Version are required; the rest are display metadata.
//
// Instructions are not part of an implementation's identity. The spec
// carries them on InitializeResult and DiscoverResult, and a server sets
// them with server.WithInstructions.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
	// Description says what this implementation does (2025-11-25).
	Description string `json:"description,omitempty"`
	// WebsiteURL is the implementation's home page (2025-11-25).
	WebsiteURL string `json:"websiteUrl,omitempty"`
	// Icons are visual identifiers a host may render (2025-11-25).
	Icons []Icon `json:"icons,omitempty"`
}

// ServerInfo is the name mcpkit used for Implementation in the serverInfo
// slot before the two were unified. It is an alias, so values are
// interchangeable.
type ServerInfo = Implementation

// ClientInfo is the name mcpkit used for Implementation in the clientInfo
// slot. It is an alias, so values are interchangeable.
type ClientInfo = Implementation

// Icon theme values for Icon.Theme.
const (
	IconThemeLight = "light"
	IconThemeDark  = "dark"
)

// Icon is one visual identifier. Src is an https: or data: URI; a consumer
// must treat the icon as untrusted input (spec "icons" section).
type Icon struct {
	Src string `json:"src"`
	// MIMEType overrides the type the server reports for Src.
	MIMEType string `json:"mimeType,omitempty"`
	// Sizes lists sizes such as "48x48", or "any" for scalable formats.
	Sizes []string `json:"sizes,omitempty"`
	// Theme is IconThemeLight or IconThemeDark when the icon is drawn for
	// that background; empty means any.
	Theme string `json:"theme,omitempty"`
}

// ElicitationFormCap is a marker for form-mode elicitation support.
// Currently empty; future specs may add fields (e.g., schema constraints).
type ElicitationFormCap struct{}

// ElicitationURLCap is a marker for URL-mode elicitation support (SEP-1036).
// Currently empty; future specs may add fields (e.g., allowed domains).
type ElicitationURLCap struct{}

// ElicitationCap describes client support for elicitation modes.
// Per SEP-1036: Form is the default mode (JSON schema → form).
// URL mode enables out-of-band interactions where the user visits a URL.
type ElicitationCap struct {
	Form *ElicitationFormCap `json:"form,omitempty"`
	URL  *ElicitationURLCap  `json:"url,omitempty"`
}

// ClientCapabilities describes features the client supports.
type ClientCapabilities struct {
	Sampling    *struct{}       `json:"sampling,omitempty"`
	Roots       *RootsCap       `json:"roots,omitempty"`
	Elicitation *ElicitationCap `json:"elicitation,omitempty"`
	Tasks       *ClientTasksCap `json:"tasks,omitempty"`
	// FileInputs advertises SEP-2356 support: the client can render
	// x-mcp-file schema properties as file pickers and encode the
	// chosen files as RFC 2397 data URIs. Currently a marker — the
	// SEP does not define sub-fields yet.
	FileInputs *struct{} `json:"fileInputs,omitempty"`

	// Extensions maps extension IDs to the client's capability declaration
	// for that extension. Sent during initialize to advertise extension support.
	Extensions map[string]ClientExtensionCap `json:"extensions,omitempty"`
}

// ClientExtensionCap describes a client's support for a specific extension.
// The contents are extension-specific; MCP Apps uses MIMETypes to declare
// which app content types the client can render.
type ClientExtensionCap struct {
	// MIMETypes lists content types the client supports for this extension.
	MIMETypes []string `json:"mimeTypes,omitempty"`
}

// RootsCap describes the client's roots capability.
type RootsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Root represents a filesystem root provided by the client.
// Roots inform the server about available directories for tool execution.
type Root struct {
	// URI is the root's location (e.g., "file:///home/user/project").
	URI string `json:"uri"`

	// Name is an optional human-readable label for this root.
	Name string `json:"name,omitempty"`
}

// RootsListResult is the response to a roots/list server-to-client request.
//
// Deprecated: per SEP-2577. Retained in 0.4; removal deferred to a future release (~2027 at the earliest, issue 850). See docs/SEP_2577_DEPRECATIONS.md.
type RootsListResult struct {
	Roots []Root `json:"roots"`
}

// ServerCapabilities describes the features the server supports,
// returned in the initialize response.
type ServerCapabilities struct {
	Tools       *ToolsCap                      `json:"tools,omitempty"`
	Resources   *ResourcesCap                  `json:"resources,omitempty"`
	Prompts     *PromptsCap                    `json:"prompts,omitempty"`
	Tasks       *TasksCap                      `json:"tasks,omitempty"`
	Logging     *struct{}                      `json:"logging,omitempty"`
	Completions *struct{}                      `json:"completions,omitempty"`
	Extensions  map[string]ExtensionCapability `json:"extensions,omitempty"`

	// TopLevel carries extension-contributed keys rendered at the top level
	// of the capabilities object (see Extension.TopLevel). MarshalJSON merges
	// them in and drops any key a field above already claims, set or not.
	TopLevel map[string]any `json:"-"`
}

// coreCapabilityKeys is the set of JSON names ServerCapabilities' own fields
// use, read off the struct tags so a field added later is reserved too.
var coreCapabilityKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(ServerCapabilities{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

// MarshalJSON renders the capabilities object, merging TopLevel keys in when
// there are any. Without TopLevel the output is exactly the default encoding.
func (c ServerCapabilities) MarshalJSON() ([]byte, error) {
	type plain ServerCapabilities
	raw, err := json.Marshal(plain(c))
	if err != nil || len(c.TopLevel) == 0 {
		return raw, err
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, err
	}
	for key, value := range c.TopLevel {
		if coreCapabilityKeys[key] {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		merged[key] = encoded
	}
	return json.Marshal(merged)
}

// ToolsCap describes the server's tools capability.
type ToolsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// EventsCap is the MCP Events extension's settings object, the value stored
// under `capabilities.extensions["io.modelcontextprotocol/events"]`.
//
// Events declares through SEP-2133 Extension Negotiation like every other
// extension here, so this is a settings object rather than a top-level
// capability. It did not always: the design sketch originally put `events`
// at the top level of `capabilities`, mcpkit implemented that in #1416, and
// the sketch's author confirmed the extensions map after two implementations
// disagreed. The PR that would have fixed the sketch (upstream PR 7) closed
// unmerged; SEP-3415 § Capability Negotiation is the authority now. See
// experimental/ext/events for the provider that registers it.
type EventsCap struct {
	// ListChanged reports whether the server sends
	// notifications/events/list_changed when its set of event types changes.
	//
	// Optional, default false, and load-bearing in both directions: the spec
	// says the notification is sent only by a server that declared true here,
	// so a server that declares false must not send it.
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCap describes the server's resources capability.
type ResourcesCap struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCap describes the server's prompts capability.
type PromptsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeResult is the typed result for the initialize response.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	// Instructions is natural-language guidance on using the server, which
	// a host may add to the model's system prompt. Omitted when empty.
	Instructions string `json:"instructions,omitempty"`
}

// ExtensionCapability is one extension's settings object as it appears in
// capabilities.extensions, keyed by the extension identifier.
//
// SEP-2133 (Final) defines extensions as "a map of extension identifiers to
// per-extension settings objects", so the settings sit directly under the
// identifier with no envelope around them. SEP-2640 (Skills) matches that
// shape for its directoryRead flag:
//
//	"extensions": {"io.modelcontextprotocol/skills": {"directoryRead": true}}
//
// Each extension defines its own key schema under its own reverse-domain
// identifier, so the value type is deliberately untyped. Callers should read
// unknown keys permissively.
//
// A nil capability marshals to {} rather than null, because SEP-2133 gives the
// empty object the specific meaning "no settings" and most extensions declare
// none.
type ExtensionCapability map[string]any

// MarshalJSON renders a nil capability as the empty object. Without it a nil
// map would emit null, which SEP-2133 does not define as a settings value.
func (e ExtensionCapability) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]any(e))
}

// StreamableHTTPAccept is the Accept header value for Streamable HTTP requests.
// Per MCP spec: clients MUST include both application/json and text/event-stream.
const StreamableHTTPAccept = "application/json, text/event-stream"

// DraftProtocolVersion2026V1 names the in-flight draft series that carries
// the next round of MCP SEPs (2243, 2549, 2356, 2567, 2575, 2663, ...) ahead
// of their inclusion in a dated stable. Shared by every draft-targeted SEP,
// not specific to any one of them — see core/stateless.go for the SEP-2575
// stateless-wire surfaces that key off this version string.
//
// The CONSTANT NAME labels the draft window. The VALUE is the dated wire
// string the spec adopted in modelcontextprotocol/spec PR 331 (merged
// 2026-06-08): the prior DRAFT placeholder was replaced with the dated
// "2026-07-28" form, which is the literal that now appears on the wire in
// `MCP-Protocol-Version` headers and `_meta.io.modelcontextprotocol/protocolVersion`.
// Treat this constant as the source of truth — never hardcode either form.
const DraftProtocolVersion2026V1 = "2026-07-28"
