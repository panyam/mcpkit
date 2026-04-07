package mcpkit

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
}

// RootsCap describes the client's roots capability.
type RootsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// StreamableHTTPAccept is the Accept header value for Streamable HTTP requests.
// Per MCP spec: clients MUST include both application/json and text/event-stream.
const StreamableHTTPAccept = "application/json, text/event-stream"
