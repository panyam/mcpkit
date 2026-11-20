package core

import (
	"fmt"
	"regexp"
	"strings"
)

// Server Card wire types from the Server Card extension (SEP-2127), schema
// at modelcontextprotocol/ext-server-card@28db12c9. A card is a static,
// public document a host reads before it connects. It carries identity,
// transport and protocol versions, never primitives.

const (
	// ServerCardMediaType is the media type a card is served and requested as.
	ServerCardMediaType = "application/mcp-server-card+json"

	// ServerCardSchemaURL is the only value the v1 schema allows in $schema.
	ServerCardSchemaURL = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"

	// ServerCardPath is the suffix the spec reserves on the streamable HTTP
	// URL: a server at https://host/mcp serves its card at
	// https://host/mcp/server-card.
	ServerCardPath = "/server-card"

	// RemoteTypeStreamableHTTP and RemoteTypeSSE are the Remote.Type values.
	RemoteTypeStreamableHTTP = "streamable-http"
	RemoteTypeSSE            = "sse"
)

// ServerCard describes a remote MCP server for pre-connection discovery.
// Its fields are advisory. A client must not use them for security
// decisions, and should check them against server/discover once connected.
type ServerCard struct {
	// Schema must be ServerCardSchemaURL.
	Schema string `json:"$schema"`
	// Name is reverse-DNS with exactly one slash, such as
	// "io.github.acme/weather". It is stricter than Implementation.Name.
	Name    string `json:"name"`
	Version string `json:"version"`
	// Description is required and 1 to 100 characters.
	Description string         `json:"description"`
	Title       string         `json:"title,omitempty"`
	WebsiteURL  string         `json:"websiteUrl,omitempty"`
	Repository  *Repository    `json:"repository,omitempty"`
	Icons       []Icon         `json:"icons,omitempty"`
	Remotes     []Remote       `json:"remotes,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// Repository points at the server's source code.
type Repository struct {
	URL string `json:"url"`
	// Source names the hosting service, such as "github".
	Source string `json:"source"`
	// Subfolder is a clean relative path inside a monorepo.
	Subfolder string `json:"subfolder,omitempty"`
	// ID is the forge's stable repository id. It changes when a repository
	// is deleted and recreated under the same name.
	ID string `json:"id,omitempty"`
}

// Remote describes one HTTP endpoint a client can connect to.
type Remote struct {
	// Type is RemoteTypeStreamableHTTP or RemoteTypeSSE.
	Type string `json:"type"`
	// URL starts with http://, https:// or a {variable} that Variables fills.
	URL                       string           `json:"url"`
	Headers                   []KeyValueInput  `json:"headers,omitempty"`
	Variables                 map[string]Input `json:"variables,omitempty"`
	SupportedProtocolVersions []string         `json:"supportedProtocolVersions,omitempty"`
	// ExtensionsInfo maps an extension id to server-level data that
	// extension defines, such as the events webhook signing keys. An entry
	// does not declare support for the extension. Support is still
	// negotiated through capabilities.extensions.
	ExtensionsInfo map[string]map[string]any `json:"extensionsInfo,omitempty"`
}

// Input describes a value a client fills in or a user is prompted for.
type Input struct {
	Description string `json:"description,omitempty"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
	// Format is "string", "number", "boolean" or "filepath". Empty means string.
	Format      string   `json:"format,omitempty"`
	Default     string   `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Value       string   `json:"value,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

// KeyValueInput is a named Input, used for HTTP headers.
type KeyValueInput struct {
	Input
	Name      string           `json:"name"`
	Variables map[string]Input `json:"variables,omitempty"`
}

var (
	serverCardNameRE = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)
	remoteURLRE      = regexp.MustCompile(`^(https?://[^\s]+|\{[a-zA-Z_][a-zA-Z0-9_]*\}[^\s]*)$`)
)

// Validate checks the constraints the v1 schema puts on a card. It returns
// the first violation found, naming the field.
func (c *ServerCard) Validate() error {
	if c.Schema != ServerCardSchemaURL {
		return fmt.Errorf("server card $schema must be %s, got %q", ServerCardSchemaURL, c.Schema)
	}
	if n := len(c.Name); n < 3 || n > 200 || !serverCardNameRE.MatchString(c.Name) {
		return fmt.Errorf("server card name %q must be reverse-DNS namespace/name, 3 to 200 chars", c.Name)
	}
	if c.Version == "" || len(c.Version) > 255 {
		return fmt.Errorf("server card version must be 1 to 255 chars, got %q", c.Version)
	}
	if strings.ContainsAny(c.Version[:1], "^~><=") || strings.HasSuffix(c.Version, ".x") || strings.HasSuffix(c.Version, ".*") {
		return fmt.Errorf("server card version %q must be a version, not a range", c.Version)
	}
	if n := len([]rune(c.Description)); n < 1 || n > 100 {
		return fmt.Errorf("server card description must be 1 to 100 chars, got %d", n)
	}
	if c.Title != "" && len([]rune(c.Title)) > 100 {
		return fmt.Errorf("server card title must be at most 100 chars")
	}
	if c.Repository != nil && (c.Repository.URL == "" || c.Repository.Source == "") {
		return fmt.Errorf("server card repository needs url and source")
	}
	for i, r := range c.Remotes {
		if r.Type != RemoteTypeStreamableHTTP && r.Type != RemoteTypeSSE {
			return fmt.Errorf("server card remote %d has type %q, want %q or %q", i, r.Type, RemoteTypeStreamableHTTP, RemoteTypeSSE)
		}
		if !remoteURLRE.MatchString(r.URL) {
			return fmt.Errorf("server card remote %d url %q must start with http://, https:// or a {variable}", i, r.URL)
		}
	}
	return nil
}
