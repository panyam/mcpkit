package core

// Protocol-level types shared between server and client packages.
// These are MCP spec types that appear in both directions of communication.

// ServerInfo identifies an MCP server in the initialize response.
type ServerInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	WebsiteURL   string `json:"websiteUrl,omitempty"`
}

// ClientInfo identifies an MCP client from the initialize request.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities describes features the client supports.
type ClientCapabilities struct {
	Sampling    *struct{} `json:"sampling,omitempty"`
	Roots       *RootsCap `json:"roots,omitempty"`
	Elicitation *struct{} `json:"elicitation,omitempty"`

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

// ServerCapabilities describes the features the server supports,
// returned in the initialize response.
type ServerCapabilities struct {
	Tools       *ToolsCap       `json:"tools,omitempty"`
	Resources   *ResourcesCap   `json:"resources,omitempty"`
	Prompts     *PromptsCap     `json:"prompts,omitempty"`
	Logging     *struct{}       `json:"logging,omitempty"`
	Completions *struct{}       `json:"completions,omitempty"`
	Extensions  map[string]ExtensionCapability `json:"extensions,omitempty"`
}

// ToolsCap describes the server's tools capability.
type ToolsCap struct {
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
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ExtensionCapability describes a server extension's metadata in the
// initialize response capabilities.
type ExtensionCapability struct {
	SpecVersion string `json:"specVersion"`
	Stability   string `json:"stability"`
}

// StreamableHTTPAccept is the Accept header value for Streamable HTTP requests.
// Per MCP spec: clients MUST include both application/json and text/event-stream.
const StreamableHTTPAccept = "application/json, text/event-stream"
