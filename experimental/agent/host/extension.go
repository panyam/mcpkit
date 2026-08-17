package host

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/experimental/agent"
)

// Extension contributes capability to an App across every seam at once.
//
// It exists because App had no way to *add* anything. All the other
// AppOptions replace something already pluggable — the provider, the stores,
// the observer, the prompt builder — so a caller who wanted their own tools
// plus the harness behaviour around them had nowhere to put it. That absence
// is why App accumulated a feature per field instead of a registry per seam.
//
// Most of this is not a new mechanism. ToolSource, ToolMiddleware,
// PromptSection, Command, and ContextStage all already existed and were
// already wired. An Extension's job is to let one package contribute across
// them as a unit, because a feature is not a handful of independent
// contributions — it is one thing with several facets that have to agree.
// Memory's recall tool and its pre-turn producer share a store; a checkpoint
// middleware and its /undo command share a snapshot directory; a file tool and
// the way its approval reads describe the same operation. Registering those
// separately would put the coherence in the caller's head and leave their
// ordering unstated.
//
// Two seams were added because building real extensions found the contract
// short: TurnStart, which checkpoint needed and no contribution seam could
// express, and ApprovalRenderers, which the coding surface needed because the
// ask text was the one part of the approval ladder a caller could not reach.
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

	// ApprovalRenderers supply the question a user is asked before a gated
	// tool call runs, for the tools this extension owns.
	//
	// The ask text was the one part of the approval ladder a caller could not
	// influence: mode, per-tool rules, remembering, and the ask transport are
	// all configurable, while the wording was fixed. That is fine until a
	// tool's arguments ARE the thing being reviewed. For an edit, the default
	// renders a JSON blob trimmed to 200 characters, which makes a real change
	// unreviewable by construction, so the user is answering a question they
	// cannot actually evaluate.
	//
	// Consulted in registration order; the first renderer to claim a call
	// wins, and the built-in format is the fallback. See ApprovalRenderer.
	ApprovalRenderers() []ApprovalRenderer

	// TurnStart runs once at the top of every turn, before any context stage
	// and before history is touched, so a failure leaves nothing half-done.
	// An error aborts the turn.
	//
	// It exists because the other seams are all contributions and none of
	// them is a lifecycle hook. An extension whose state is scoped to a turn
	// — a checkpoint to restore to, a per-turn budget, a counter — had
	// nowhere to learn a turn had begun, and the nearest workaround was a
	// ContextStage that produces no context and exists for its side effect.
	// A stage that lies about being a producer is worse than a seam that
	// admits what it is.
	//
	// Proactive turns (a trigger firing) call this too: they run the model
	// over history exactly as a user turn does, so an extension that skipped
	// them would hold state scoped to the wrong thing.
	TurnStart(ctx context.Context) error

	// Close releases what the extension owns, and is called by App.Close in
	// reverse registration order so an extension is torn down before whatever
	// it was registered after.
	//
	// TurnStart had no counterpart until an extension owned something the
	// runtime could not clean up for it. A directory handle survives being
	// forgotten and a snapshot directory is meant to outlive the process, so
	// the first two extensions needed nothing here. A subprocess is different:
	// agent/ext/lsp spawns a language server, and one that outlives the App
	// holds a workspace lock and a share of memory nothing can account for.
	//
	// Errors are collected and reported rather than aborting the sweep, since
	// one extension that cannot close is not a reason to leak the rest.
	Close() error
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

func (BaseExtension) Tools() (agent.ToolSource, error)      { return nil, nil }
func (BaseExtension) Middleware() []agent.ToolMiddleware    { return nil }
func (BaseExtension) PromptSections() []PromptSection       { return nil }
func (BaseExtension) Commands() []*Command                  { return nil }
func (BaseExtension) ContextStages() []ContextStage         { return nil }
func (BaseExtension) ApprovalRenderers() []ApprovalRenderer { return nil }
func (BaseExtension) TurnStart(context.Context) error       { return nil }
func (BaseExtension) Close() error                          { return nil }

// ApprovalRenderer turns a pending tool call into the question a user is
// asked about it, or declines to.
//
// The bool is "this call is mine". False means the next renderer gets a look
// and the built-in format is the fallback, so a renderer can claim a subset of
// its own tools, or claim conditionally, without having to reproduce the
// default for everything else. There is no error return on purpose: a
// renderer that cannot do its job returns false and the user still gets asked,
// which is the only useful behaviour at an approval prompt.
//
// The info carries the call as it WILL execute, arguments included, after
// every middleware has rewritten them (#1248). That property is what makes the
// prompt honest — the user approves the call that runs, not the one the model
// proposed — and it is pinned by a test rather than left as a comment.
//
// Truncation is the renderer's business. The default trims arguments so a
// large payload cannot flood the prompt; a renderer showing a diff wants the
// opposite and is trusted to decide.
//
// The context is the turn's. It is here so a renderer can read state it needs
// to render honestly — showing what a whole-file write would replace means
// reading the file that is there now — rather than being restricted to what
// the arguments happen to carry.
type ApprovalRenderer func(context.Context, agent.ToolCallInfo) (string, bool)

// WithExtension registers extensions with the App, applied in the order
// given. Repeated calls accumulate rather than replace, so a caller can build
// the list across several options.
func WithExtension(exts ...Extension) AppOption {
	return func(o *appOptions) { o.extensions = append(o.extensions, exts...) }
}

// renderApproval produces the question to ask about a pending call, giving
// each registered renderer a look before falling back to the built-in format.
//
// First claim wins, in registration order, matching how middleware is ordered
// rather than how Commands collide. Commands can refuse a duplicate because a
// name is known at registration; a renderer decides per call, so two claiming
// the same one cannot be detected until it happens, and failing a turn at that
// point would be worse than picking the first.
//
// A renderer that returns an empty string is treated as having declined,
// because an empty approval prompt asks the user to confirm nothing.
func (a *App) renderApproval(ctx context.Context, info agent.ToolCallInfo) string {
	for _, r := range a.approvalRenderers {
		if r == nil {
			continue
		}
		if text, ok := r(ctx, info); ok && text != "" {
			return text
		}
	}
	return approvalPrompt(info)
}

// startExtensionTurns runs every extension's TurnStart, in registration
// order, stopping at the first error.
//
// Called with turnMu held and before history is touched, so an extension that
// cannot start its turn aborts before anything is half-applied. A failure
// names the extension: a turn that dies on someone else's bookkeeping should
// say whose.
func (a *App) startExtensionTurns(ctx context.Context) error {
	for _, ext := range a.extensions {
		if err := ext.TurnStart(ctx); err != nil {
			return fmt.Errorf("host: extension %q turn start: %w", ext.Name(), err)
		}
	}
	return nil
}

// closeExtensions closes every extension, in reverse registration order.
//
// Reverse because registration order is dependency order everywhere else: an
// extension registered later was built knowing the earlier ones were there,
// which is the ordering surfaces.WorkspaceExtensions relies on when it puts
// checkpoint ahead of the tools it snapshots. Tearing down forwards would
// close a dependency out from under its dependent.
//
// Every extension is closed even after one fails, and the failures are logged
// rather than returned, because App.Close is the last thing a surface does and
// has nowhere to report to. One extension that cannot close is not a reason to
// leak the others.
// Each extension is closed exactly once even if App.Close is called twice,
// which a surface does by hand often enough (a deferred Close plus an explicit
// one on a shutdown path) that leaving it to every implementation to guard
// would make Close the one seam nobody could write simply.
func (a *App) closeExtensions() {
	exts := a.extensions
	a.extensions = nil
	for i := len(exts) - 1; i >= 0; i-- {
		if err := exts[i].Close(); err != nil && a.log != nil {
			a.log.Warn("extension close failed", "extension", exts[i].Name(), "err", err)
		}
	}
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
	a.extensions = exts
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
			// Deliberately not recorded as operator. An extension is
			// arbitrary code that may shell out or fetch, so the host has no
			// standing to vouch for its output; config must say so
			// explicitly. See derivedProvenance.
			if err := a.sources.Add(name, src); err != nil {
				return nil, fmt.Errorf("host: extension %q tools: %w", name, err)
			}
		}

		mw = append(mw, ext.Middleware()...)
		a.promptBuilder.Sections = append(a.promptBuilder.Sections, ext.PromptSections()...)
		a.context.transient = append(a.context.transient, ext.ContextStages()...)
		a.approvalRenderers = append(a.approvalRenderers, ext.ApprovalRenderers()...)

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
