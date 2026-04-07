package core

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

// AppMIMEType is the MIME type for MCP App HTML resources.
// This profile parameter distinguishes MCP App HTML from regular HTML —
// hosts use it to decide whether to render in a sandboxed iframe.
const AppMIMEType = "text/html;profile=mcp-app"

// ToolMeta holds protocol-level metadata for a tool definition.
// Serialized as "_meta" in the tools/list response.
type ToolMeta struct {
	// UI contains MCP Apps presentation metadata.
	UI *UIMetadata `json:"ui,omitempty"`
}

// ResourceContentMeta holds per-content metadata in resources/read responses.
// Takes precedence over the resource-level metadata from resources/list.
type ResourceContentMeta struct {
	// UI contains MCP Apps presentation metadata.
	UI *UIMetadata `json:"ui,omitempty"`
}
