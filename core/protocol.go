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
type RootsListResult struct {
	Roots []Root `json:"roots"`
}

// ServerCapabilities describes the features the server supports,
// returned in the initialize response.
type ServerCapabilities struct {
	Tools       *ToolsCap       `json:"tools,omitempty"`
	Resources   *ResourcesCap   `json:"resources,omitempty"`
	Prompts     *PromptsCap     `json:"prompts,omitempty"`
	Tasks       *TasksCap       `json:"tasks,omitempty"`
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
