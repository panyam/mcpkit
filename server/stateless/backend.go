package stateless

import (
	core "github.com/panyam/mcpkit/core"
)

// Backend is the read-only registry + server-metadata surface the
// dispatcher needs. The parent server package provides an adapter
// wrapping its *Registry and Server fields; tests provide fakes.
//
// Defining the surface here (not in server) keeps the import direction
// one-way: server depends on stateless, never the reverse. When legacy
// goes away, this Backend interface stays — the future "stateless-only"
// server will still satisfy it.
type Backend interface {
	// ServerInfo returns the implementation name/version pair advertised
	// in server/discover.
	ServerInfo() core.ServerInfo

	// Capabilities returns the capability shape advertised by this server
	// in server/discover. The dispatcher does not mutate it; the backend
	// builds it from registered tools/resources/prompts/extensions.
	Capabilities() core.ServerCapabilities

	// SupportedVersions returns the protocol versions this server speaks
	// on the stateless wire. Always includes core.DraftProtocolVersion2026V1.
	SupportedVersions() []string

	// Tools returns a snapshot of registered tool definitions, in
	// registration order. Mutations made after the call are not visible.
	Tools() []core.ToolDef

	// Tool returns the definition + handler for a registered tool, or
	// ok=false if no such tool. Used by tools/call dispatch.
	Tool(name string) (core.ToolDef, core.ToolHandler, bool)

	// Resources returns a snapshot of registered resource definitions.
	Resources() []core.ResourceDef

	// Resource returns def + handler for a concrete resource URI.
	Resource(uri string) (core.ResourceDef, core.ResourceHandler, bool)

	// ResourceTemplates returns a snapshot of registered templates.
	ResourceTemplates() []core.ResourceTemplate

	// ResourceTemplate returns def + handler for a template URI.
	ResourceTemplate(uriTemplate string) (core.ResourceTemplate, core.TemplateHandler, bool)

	// Prompts returns a snapshot of registered prompt definitions.
	Prompts() []core.PromptDef

	// Prompt returns def + handler for a prompt name.
	Prompt(name string) (core.PromptDef, core.PromptHandler, bool)

	// Completion returns a registered completion handler for the
	// (refType, name) pair, e.g. ("ref/prompt", "summarize").
	Completion(refType, name string) (core.CompletionHandler, bool)

	// ListTTLMs / ListCacheScope are the SEP-2549 cache hints applied
	// to every list response. A nil *int / empty string omits the field.
	ListTTLMs() *int
	ListCacheScope() string
}
