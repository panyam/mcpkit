package core

import (
	"context"
	"encoding/json"
)

// UIExtensionID is the extension identifier for MCP Apps.
// Used in initialize handshake for both server advertisement and client capability declaration.
const UIExtensionID = "io.modelcontextprotocol/ui"

// ClientSupportsUI checks whether the connected client declared support for the
// MCP Apps extension during the initialize handshake. Tool handlers can use this
// to decide whether to include UI-specific content or fall back to text-only.
func ClientSupportsUI(ctx context.Context) bool {
	return ClientSupportsExtension(ctx, UIExtensionID)
}

// UIMetadata describes UI presentation metadata for tools and resources.
// Serialized as the "_meta.ui" object in tools/list and resources/read responses.
// Part of the MCP Apps extension (io.modelcontextprotocol/ui).
type UIMetadata struct {
	// ResourceUri points to a ui:// resource containing the HTML to render.
	// Required for tools that want inline UI rendering.
	ResourceUri string `json:"resourceUri,omitempty"`

	// Visibility controls who can see/call this tool.
	// Default (nil): host applies its own default (typically ["model", "app"]).
	Visibility []UIVisibility `json:"visibility,omitempty"`

	// CSP declares external domains the app needs to load resources from.
	// Hosts construct Content-Security-Policy from these declarations.
	CSP *UICSPConfig `json:"csp,omitempty"`

	// Permissions lists browser capabilities the app requests
	// (e.g., "camera", "microphone", "geolocation", "clipboardWrite").
	Permissions []string `json:"permissions,omitempty"`

	// PrefersBorder hints whether the host should draw a visible border.
	// nil = host decides, true = border, false = no border.
	PrefersBorder *bool `json:"prefersBorder,omitempty"`

	// Domain requests a dedicated sandbox origin for the app.
	// Format is host-dependent (e.g., "myapp" → myapp.claudemcpcontent.com).
	Domain string `json:"domain,omitempty"`

	// SupportedDisplayModes declares which display modes this app can render in.
	// Nil means the host decides. Hosts use this to offer mode switching UI.
	SupportedDisplayModes []DisplayMode `json:"supportedDisplayModes,omitempty"`
}

// UICSPConfig declares external domains for Content-Security-Policy construction.
// Hosts use these declarations when sandboxing MCP App iframes.
type UICSPConfig struct {
	// ConnectDomains → CSP connect-src (fetch, XHR, WebSocket targets).
	ConnectDomains []string `json:"connectDomains,omitempty"`

	// ResourceDomains → CSP script-src, style-src, img-src, font-src, media-src.
	ResourceDomains []string `json:"resourceDomains,omitempty"`

	// FrameDomains → CSP frame-src (nested iframes).
	FrameDomains []string `json:"frameDomains,omitempty"`

	// BaseUriDomains → CSP base-uri.
	BaseUriDomains []string `json:"baseUriDomains,omitempty"`
}

// UIVisibility controls tool access scope in the MCP Apps extension.
type UIVisibility string

const (
	// UIVisibilityModel means the tool appears in tools/list for the LLM.
	UIVisibilityModel UIVisibility = "model"

	// UIVisibilityApp means the tool is callable by apps from the same server.
	UIVisibilityApp UIVisibility = "app"
)

// DisplayMode represents an iframe display mode for MCP Apps.
type DisplayMode string

const (
	// DisplayModeInline renders the app inline within the host UI.
	DisplayModeInline DisplayMode = "inline"

	// DisplayModeFullscreen renders the app in a fullscreen overlay.
	DisplayModeFullscreen DisplayMode = "fullscreen"

	// DisplayModePIP renders the app in a picture-in-picture window.
	DisplayModePIP DisplayMode = "pip"
)

// AppMIMEType is the MIME type for MCP App HTML resources.
// This profile parameter distinguishes MCP App HTML from regular HTML —
// hosts use it to decide whether to render in a sandboxed iframe.
const AppMIMEType = "text/html;profile=mcp-app"

// ToolMeta holds protocol-level metadata for a tool definition.
// Serialized as "_meta" in the tools/list response.
//
// Custom MarshalJSON/UnmarshalJSON mirror upstream `ext-apps`'s
// `registerAppTool` (`RESOURCE_URI_META_KEY` constant): when UI.ResourceUri
// is set, BOTH `ui.resourceUri` (nested) and `ui/resourceUri` (flat) keys
// are emitted on the wire. Unmarshaling accepts either form; if both are
// present they must agree (or the nested form wins). This keeps mcpkit
// tools interoperable with hosts that read either the older flat key
// convention or the newer nested form.
type ToolMeta struct {
	// UI contains MCP Apps presentation metadata.
	UI *UIMetadata `json:"ui,omitempty"`
}

// metaResourceURIKey is the flat-form fallback key emitted alongside
// _meta.ui.resourceUri for backward-compatibility with hosts written against
// older ext-apps spec drafts. Mirrors upstream's RESOURCE_URI_META_KEY.
const metaResourceURIKey = "ui/resourceUri"

// MarshalJSON serializes ToolMeta so that UI.ResourceUri appears under BOTH
// the nested ui.resourceUri key and the flat ui/resourceUri key, matching
// upstream ext-apps's emit. Other UI fields stay under the nested form only.
func (m ToolMeta) MarshalJSON() ([]byte, error) {
	type alias ToolMeta
	// Without UI, behave like default struct marshal — emits empty object.
	if m.UI == nil {
		return json.Marshal(alias(m))
	}
	out := map[string]json.RawMessage{}
	uiBytes, err := json.Marshal(m.UI)
	if err != nil {
		return nil, err
	}
	out["ui"] = uiBytes
	if m.UI.ResourceUri != "" {
		ruBytes, err := json.Marshal(m.UI.ResourceUri)
		if err != nil {
			return nil, err
		}
		out[metaResourceURIKey] = ruBytes
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts either the nested ui.resourceUri form, the flat
// ui/resourceUri form, or both. If both are present and disagree, the nested
// form wins (callers reading the flat-form-only branch would have missed
// any other UI fields anyway, so the nested form is the more complete one).
func (m *ToolMeta) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.UI = nil
	if uiBytes, ok := raw["ui"]; ok {
		var ui UIMetadata
		if err := json.Unmarshal(uiBytes, &ui); err != nil {
			return err
		}
		m.UI = &ui
	}
	if flatBytes, ok := raw[metaResourceURIKey]; ok {
		var flat string
		if err := json.Unmarshal(flatBytes, &flat); err != nil {
			return err
		}
		if m.UI == nil {
			m.UI = &UIMetadata{ResourceUri: flat}
		} else if m.UI.ResourceUri == "" {
			m.UI.ResourceUri = flat
		}
		// If both present and disagree, nested form already in m.UI wins —
		// see godoc above.
	}
	return nil
}

// ResourceContentMeta holds per-content metadata in resources/read responses.
// Takes precedence over the resource-level metadata from resources/list.
type ResourceContentMeta struct {
	// UI contains MCP Apps presentation metadata.
	UI *UIMetadata `json:"ui,omitempty"`
}
