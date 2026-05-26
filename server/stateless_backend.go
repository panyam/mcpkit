package server

import (
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// statelessBackend adapts a *Server (with its embedded Dispatcher and
// Registry) to the stateless.Backend interface. Lives in package server
// rather than server/stateless so it can read registry private fields
// without exporting more API surface.
//
// One instance per Server, constructed by newStreamableTransport when
// statelessMode != stateless.ModeLegacyOnly.
type statelessBackend struct {
	s *Server
}

// newStatelessBackend builds a Backend bound to the given server.
func newStatelessBackend(s *Server) *statelessBackend {
	return &statelessBackend{s: s}
}

// Compile-time check: the adapter satisfies the stateless package's
// interface. If the interface ever gains a method, this line breaks
// first — loudest signal we can give.
var _ stateless.Backend = (*statelessBackend)(nil)

// reg is a small accessor to keep the read sites tidy. The Registry
// lives on the legacy session Dispatcher today; we read through it
// for now. When the legacy dispatcher is retired, this accessor moves
// to whatever owns the shared registry post-cleanup.
func (b *statelessBackend) reg() *Registry { return b.s.dispatcher.Reg }

// ServerInfo returns the implementation identity advertised in
// server/discover responses. Sourced from NewServer's first argument.
func (b *statelessBackend) ServerInfo() core.ServerInfo {
	return b.s.dispatcher.serverInfo
}

// Capabilities returns the capability shape derived from registered
// handlers + active extensions. Mirrors the legacy initialize handshake's
// computation modulo session-only fields.
func (b *statelessBackend) Capabilities() core.ServerCapabilities {
	caps := core.ServerCapabilities{}

	r := b.reg()
	r.mu.RLock()
	hasTools := len(r.tools) > 0
	hasResources := len(r.resources) > 0 || len(r.templates) > 0
	hasPrompts := len(r.prompts) > 0
	hasCompletions := len(r.completions) > 0
	r.mu.RUnlock()

	if hasTools {
		caps.Tools = &core.ToolsCap{ListChanged: true}
	}
	if hasResources {
		caps.Resources = &core.ResourcesCap{ListChanged: true}
	}
	if hasPrompts {
		caps.Prompts = &core.PromptsCap{ListChanged: true}
	}
	if hasCompletions {
		caps.Completions = &struct{}{}
	}

	if exts := b.s.dispatcher.extensions; len(exts) > 0 {
		caps.Extensions = make(map[string]core.ExtensionCapability, len(exts))
		for id, e := range exts {
			caps.Extensions[id] = core.ExtensionCapability{
				SpecVersion: e.SpecVersion,
				Stability:   string(e.Stability),
			}
		}
	}
	return caps
}

// SupportedVersions returns the stateless-wire version list this build
// speaks. Distinct from the legacy initialize negotiation list — the
// two surfaces are advertised independently.
func (b *statelessBackend) SupportedVersions() []string {
	return core.SupportedStatelessVersions
}

// Tools returns a snapshot of registered tools in registration order.
func (b *statelessBackend) Tools() []core.ToolDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ToolDef, 0, len(r.toolOrder))
	for _, name := range r.toolOrder {
		if e, ok := r.tools[name]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Tool returns def + handler for a registered tool.
func (b *statelessBackend) Tool(name string) (core.ToolDef, core.ToolHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return core.ToolDef{}, nil, false
	}
	return e.def, e.handler, true
}

// Resources returns a snapshot of concrete resources in registration order.
func (b *statelessBackend) Resources() []core.ResourceDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ResourceDef, 0, len(r.resourceOrder))
	for _, uri := range r.resourceOrder {
		if e, ok := r.resources[uri]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Resource returns def + handler for a concrete resource URI.
func (b *statelessBackend) Resource(uri string) (core.ResourceDef, core.ResourceHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.resources[uri]
	if !ok {
		return core.ResourceDef{}, nil, false
	}
	return e.def, e.handler, true
}

// ResourceTemplates returns a snapshot of templates in registration order.
func (b *statelessBackend) ResourceTemplates() []core.ResourceTemplate {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ResourceTemplate, 0, len(r.templateOrder))
	for _, uri := range r.templateOrder {
		if e, ok := r.templates[uri]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// ResourceTemplate returns def + handler for a template URI.
func (b *statelessBackend) ResourceTemplate(uriTemplate string) (core.ResourceTemplate, core.TemplateHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.templates[uriTemplate]
	if !ok {
		return core.ResourceTemplate{}, nil, false
	}
	return e.def, e.handler, true
}

// Prompts returns a snapshot of prompts in registration order.
func (b *statelessBackend) Prompts() []core.PromptDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.PromptDef, 0, len(r.promptOrder))
	for _, name := range r.promptOrder {
		if e, ok := r.prompts[name]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Prompt returns def + handler for a prompt name.
func (b *statelessBackend) Prompt(name string) (core.PromptDef, core.PromptHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.prompts[name]
	if !ok {
		return core.PromptDef{}, nil, false
	}
	return e.def, e.handler, true
}

// Completion returns a registered completion handler for the
// (refType, name|uri) pair. Registry keys completions as
// "refType:key" — this adapter joins them on read.
func (b *statelessBackend) Completion(refType, key string) (core.CompletionHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.completions[refType+":"+key]
	return h, ok
}

// ListTTLMs returns the SEP-2549 ttlMs hint applied to list responses.
func (b *statelessBackend) ListTTLMs() *int {
	return b.s.options.listTTLMs
}

// ListCacheScope returns the SEP-2549 cacheScope hint applied to list responses.
func (b *statelessBackend) ListCacheScope() string {
	return b.s.options.listCacheScope
}
