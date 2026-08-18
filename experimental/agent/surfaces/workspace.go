package surfaces

import (
	"fmt"

	"github.com/panyam/mcpkit/experimental/agent/ext/checkpoint"
	"github.com/panyam/mcpkit/experimental/agent/ext/files"
	"github.com/panyam/mcpkit/experimental/agent/ext/lsp"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

// WorkspaceConfig describes the workspace tool set a surface exposes.
//
// It lives here rather than in host because agent/ext/files imports host for
// its approval renderer, so host cannot import the extensions back. Both
// agentchat and agentweb need the identical wiring, and getting it wrong is
// silent in one specific way (see WorkspaceExtensions), so it is written once.
type WorkspaceConfig struct {
	// Roots confine every path the file tools will touch. Empty disables the
	// whole set: no file tools, no checkpoint. The first is primary, and a
	// relative path resolves against it.
	//
	// A set rather than one directory because a session that stays inside a
	// single repository is the exception (issue 1314). An agent given one root
	// edits an API and reports success while the caller it broke sits in
	// another repository it cannot see.
	//
	// There is deliberately no "unset means the current directory" default.
	// files.Config.Roots documents why the tools themselves refuse to run
	// unconfined, and the same reasoning applies to choosing the confinement
	// for a user: a model's instructions can come from content it read, so an
	// editor rooted somewhere nobody named is an injected instruction away
	// from writing anywhere under it. Enabling these is a choice with
	// directories attached.
	Roots []string

	// Exclude overrides the directories list_files and search_files skip.
	// Nil means files.DefaultExclude; an explicitly empty non-nil slice means
	// exclude nothing.
	Exclude []string

	// CheckpointStore is where snapshots live. Empty puts them under the
	// primary root.
	//
	// It is separate from Roots because a snapshot store inside a workspace is
	// a directory the agent can read, search, and edit, and one that grows
	// with every write. With several roots there is also no obvious one to
	// pick. Naming a location outside them all is the cleaner arrangement and
	// is what a caller should usually do.
	CheckpointStore string

	// NoCheckpoint drops the snapshot-and-restore safety net, leaving the file
	// tools with no /undo. Opt-out rather than opt-in because a write tool
	// whose effects cannot be reversed is the case checkpoint was built for.
	NoCheckpoint bool

	// LanguageServers adds LSP-in-loop: navigation tools, and diagnostics
	// after every write and in each turn's context. Empty adds nothing.
	//
	// Opt-in, unlike checkpoint, because this one spawns processes. A
	// checkpoint costs a directory and is wanted wherever writes are, while a
	// language server the caller did not ask for is a subprocess, an index of
	// the whole tree, and a few hundred megabytes. Naming the servers is also
	// unavoidable: nothing here can guess which languages the workspace holds
	// or which server the caller wants driving them.
	LanguageServers []lsp.ServerSpec
}

// WorkspaceExtensions builds the file and checkpoint extensions for cfg.
//
// Returns nil when cfg.Root is empty, so a caller can pass the result to
// host.WithExtension unconditionally and get today's behaviour when no
// workspace was requested.
//
// # Order is load-bearing
//
// Checkpoint comes first in the returned slice. Extensions are applied in
// registration order and their middleware keeps it, so the snapshot must be
// installed ahead of the tool that writes. Reversed, the middleware captures
// the file *after* the write and /undo restores the damage.
//
// Nothing catches that. Both orders construct, both run, and the difference
// only shows up as an /undo that appears to work and does not, which is why
// the two extensions are built together here instead of being left to each
// surface to register in whatever order reads well.
//
// # Why the two are coupled
//
// Checkpoint's WriteSpec list is per-tool, so with no file tools it guards
// nothing; and file tools without it drop the reversal path that agent/ext/
// checkpoint exists to provide. Wiring them separately would let a surface
// enable half of a safety property.
//
// files.PathArg is the seam that joins them: checkpoint needs to know which
// argument names a path, files exports the reader for its own tools, and
// neither module imports the other.
func WorkspaceExtensions(cfg WorkspaceConfig) ([]host.Extension, error) {
	if len(cfg.Roots) == 0 {
		return nil, nil
	}

	var exts []host.Extension

	store := cfg.CheckpointStore
	if store == "" {
		store = cfg.Roots[0]
	}

	if !cfg.NoCheckpoint {
		cx, err := checkpoint.New(checkpoint.Config{
			Root: store,
			Writes: []checkpoint.WriteSpec{
				{Tool: "write_file", Paths: files.PathArg},
				{Tool: "edit_file", Paths: files.PathArg},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("workspace checkpoint: %w", err)
		}
		exts = append(exts, cx)
	}

	// files.New rather than files.NewSource: the extension carries the prompt
	// section describing the read-then-edit contract along with the tools.
	// Registering the tools alone leaves that contract discoverable only by
	// failing an edit.
	fx, err := files.New(files.Config{Roots: cfg.Roots, Exclude: cfg.Exclude})
	if err != nil {
		return nil, fmt.Errorf("workspace files: %w", err)
	}
	exts = append(exts, fx)

	if len(cfg.LanguageServers) > 0 {
		// Last, and for the mirror image of checkpoint's reason. Checkpoint
		// must see a file before it is written; this must see it after. Its
		// middleware therefore has to sit innermost of the three, which
		// registration order gives it.
		// lsp is still single-root (issue 1314), so it gets the primary one.
		// A language server is rooted, so spanning repositories there means an
		// instance per root per language rather than a wider path list.
		lx, lspErr := lsp.New(lsp.Config{
			Root:    cfg.Roots[0],
			Servers: cfg.LanguageServers,
			Writes: []lsp.WriteSpec{
				{Tool: "write_file", Paths: files.PathArg},
				{Tool: "edit_file", Paths: files.PathArg},
			},
		})
		if lspErr != nil {
			// Everything built so far owns nothing that outlives this
			// function except the language servers we failed to finish
			// starting, which lsp.New has already cleaned up.
			return nil, fmt.Errorf("workspace lsp: %w", lspErr)
		}
		exts = append(exts, lx)
	}

	return exts, nil
}
