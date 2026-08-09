package host

import (
	"fmt"

	"github.com/panyam/mcpkit/agent"
)

// Extension contributes capability to an App across every seam at once.
//
// It exists because App had no way to *add* anything. All the other
// AppOptions replace something already pluggable — the provider, the stores,
// the observer, the prompt builder — so a caller who wanted their own tools
// plus the harness behaviour around them had nowhere to put it. That absence
// is why App accumulated a feature per field instead of a registry per seam.
//
// Nothing here is a new mechanism. ToolSource, ToolMiddleware, PromptSection,
// Command, and ContextStage all already exist and are all already wired. An
// Extension's only job is to let one package contribute across all five as a
// unit, because a feature is not five independent contributions — it is one
// thing with five facets that have to agree. Memory's recall tool and its
// pre-turn producer share a store; a checkpoint middleware and its /undo
// command share a snapshot directory. Registering those separately would put
// the coherence in the caller's head and leave their ordering unstated.
//
// Every method except Name is optional: return nil for the seams you do not
// use. Embed BaseExtension to no-op all of them and override only what you
// need.
//
// # Ordering
//
// Extensions are applied in the order given to WithExtension, and within one
// extension each seam keeps the order its slice declares. Two orderings are
// load-bearing:
//
//   - Middleware lands before the host's own permission gate, so a gate still
//     sees the arguments an extension rewrote. See
//     agent.RunnerConfig.ToolMiddleware, where a gate belongs last.
//   - Context stages append after the built-in producers, so an extension's
//     block sits closer to the user's message than the memory summary. Later
//     is more salient; see contextPipeline.
//
// # What stays host-owned
//
// The permission gate is not an extension and should not become one. Its
// position is load-bearing rather than incidental: it must run last so it
// decides on the arguments every other middleware has already rewritten. An
// extension that could claim that slot could also take it from the gate,
// which is the one ordering nobody should be able to change by registration
// order. So TieredApproval stays wired by the host, after extension
// middleware, and an extension that wants to refuse a call returns
// agent.DenyTool from its own middleware instead.
//
// # Naming
//
// Tools register into the same MultiSource as everything else, so a tool name
// that collides with another source's resolves through the existing
// disambiguation rather than a rule invented here. Commands collide loudly:
// registering a name the registry already holds is an error, because a
// silently shadowed /command is worse than a failed startup.
type Extension interface {
	// Name identifies the extension in errors, diagnostics, and stage names.
	// It is also the source id its tools register under, so it must be unique
	// across the extensions given to one App.
	Name() string

	// Tools returns the extension's tool source, or nil for none. An error
	// fails App construction: an extension that cannot build its tools is not
	// something to start up half-configured.
	Tools() (agent.ToolSource, error)

	// Middleware wraps tool dispatch. Applied before the host's permission
	// gate; see the ordering note above.
	Middleware() []agent.ToolMiddleware

	// PromptSections contribute to the per-turn system prompt, appended after
	// the host's own sections.
	PromptSections() []PromptSection

	// Commands are slash commands every surface dispatches. A name already in
	// the registry is an error.
	Commands() []*Command

	// ContextStages are per-turn context producers, appended to the transient
	// phase. Use this for context the model should see for one turn without
	// it joining the session's history.
	ContextStages() []ContextStage
}

// BaseExtension implements every optional Extension seam as a no-op, so an
// extension embeds it and overrides only what it contributes. It deliberately
// does not supply Name: an unnamed extension is never what a caller meant.
//
//	type coding struct{ host.BaseExtension }
//
//	func (coding) Name() string { return "coding" }
//	func (c coding) Tools() (agent.ToolSource, error) { return c.src, nil }
type BaseExtension struct{}

func (BaseExtension) Tools() (agent.ToolSource, error)   { return nil, nil }
func (BaseExtension) Middleware() []agent.ToolMiddleware { return nil }
func (BaseExtension) PromptSections() []PromptSection    { return nil }
func (BaseExtension) Commands() []*Command               { return nil }
func (BaseExtension) ContextStages() []ContextStage      { return nil }

// WithExtension registers extensions with the App, applied in the order
// given. Repeated calls accumulate rather than replace, so a caller can build
// the list across several options.
func WithExtension(exts ...Extension) AppOption {
	return func(o *appOptions) { o.extensions = append(o.extensions, exts...) }
}

// applyExtensions wires every extension into the seams it contributes to.
//
// Called during construction, after the built-in producers exist so an
// extension's contributions land after theirs, and before the Runner is built
// so extension middleware reaches RunnerConfig. Middleware is returned rather
// than applied because the caller must still append the permission gate after
// it.
func (a *App) applyExtensions(exts []Extension) ([]agent.ToolMiddleware, error) {
	var mw []agent.ToolMiddleware
	seen := map[string]bool{}
	for _, ext := range exts {
		name := ext.Name()
		if name == "" {
			return nil, fmt.Errorf("host: extension has no name")
		}
		if seen[name] {
			return nil, fmt.Errorf("host: duplicate extension %q", name)
		}
		seen[name] = true

		src, err := ext.Tools()
		if err != nil {
			return nil, fmt.Errorf("host: extension %q tools: %w", name, err)
		}
		if src != nil {
			if err := a.sources.Add(name, src); err != nil {
				return nil, fmt.Errorf("host: extension %q tools: %w", name, err)
			}
		}

		mw = append(mw, ext.Middleware()...)
		a.promptBuilder.Sections = append(a.promptBuilder.Sections, ext.PromptSections()...)
		a.context.transient = append(a.context.transient, ext.ContextStages()...)

		for _, cmd := range ext.Commands() {
			// Register overwrites silently, which is the wrong behaviour for
			// a command an extension did not know was taken: a shadowed
			// /command fails at use, far from the cause. Refuse instead.
			if _, taken := a.commands.Lookup(cmd.Name); taken {
				return nil, fmt.Errorf("host: extension %q command %q is already registered", name, cmd.Name)
			}
			a.commands.Register(cmd)
		}
	}
	return mw, nil
}
