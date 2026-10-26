package core

import (
	"context"
	"path/filepath"
	"strings"
)

// Allowed-roots enforcement API (#197).
//
// Tool handlers call IsPathAllowed to check whether a file path falls
// within the session's enforced roots. The enforced roots are the
// intersection of the server's static WithAllowedRoots list and the
// client's dynamic roots from roots/list — or just the client roots if
// no static list is configured.
//
// The allowed-roots snapshot is a function (not a slice) so it can
// be recomputed cheaply when client roots change mid-session via
// notifications/roots/list_changed. The function is installed by the
// server dispatch path via SetAllowedRoots and stored on the session
// context alongside other per-session state.

// IsPathAllowed reports whether the given file path falls within at
// least one of the session's enforced roots. Returns true if:
//   - No session context is present (bare context, no sandbox)
//   - No allowed-roots function is installed (no WithAllowedRoots, no client roots)
//   - The allowed-roots function returns nil (no restriction)
//
// Returns false if the allowed-roots function returns a non-nil empty
// slice (explicit "nothing allowed") or the path doesn't match any root.
//
// Path matching uses cleaned, directory-prefix semantics: "/workspace"
// allows "/workspace/src/main.go" but NOT "/workspace-other/y". Both
// the root and the path are cleaned via filepath.Clean before comparison.
//
// Deprecated: per SEP-2577. Retained in 0.4; removal deferred to a future release (~2027 at the earliest, issue 850). See docs/SEP_2577_DEPRECATIONS.md.
func IsPathAllowed(ctx context.Context, path string) bool {
	fn := allowedRootsFunc(ctx)
	if fn == nil {
		return true
	}
	roots := fn()
	if roots == nil {
		return true
	}
	path = filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AllowedRoots returns the current enforced roots for the session, or
// nil if no roots enforcement is configured. The returned slice is the
// snapshot from the allowed-roots function — it may change on the next
// call if client roots update.
//
// Deprecated: per SEP-2577. Retained in 0.4; removal deferred to a future release (~2027 at the earliest, issue 850). See docs/SEP_2577_DEPRECATIONS.md.
func AllowedRoots(ctx context.Context) []string {
	fn := allowedRootsFunc(ctx)
	if fn == nil {
		return nil
	}
	return fn()
}

// allowedRootsFunc returns the supplier SetAllowedRoots installed, from the
// session when there is one and from ctx otherwise.
func allowedRootsFunc(ctx context.Context) func() []string {
	if sc := sessionFromContext(ctx); sc != nil {
		return sc.allowedRoots
	}
	fn, _ := ctx.Value(statelessAllowedRootsCtxKey).(func() []string)
	return fn
}

// SetAllowedRoots installs an allowed-roots supplier on the session
// stored in ctx. Exported for the server dispatch layer to wire in
// the computed root set during session setup. Call it after
// ContextWithSession; with no session in ctx (the stateless wire) the
// supplier rides on the returned ctx instead.
//
// Deprecated: per SEP-2577. Retained in 0.4; removal deferred to a future release (~2027 at the earliest, issue 850). See docs/SEP_2577_DEPRECATIONS.md.
func SetAllowedRoots(ctx context.Context, fn func() []string) context.Context {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.allowedRoots = fn
		return ctx
	}
	return context.WithValue(ctx, statelessAllowedRootsCtxKey, fn)
}

// FileURIToPath converts a file:// URI to a local filesystem path.
// Returns the path portion with the "file://" prefix stripped. Returns
// the input unchanged if it doesn't have a file:// prefix. Used by
// the server dispatch layer to convert client-provided root URIs to
// paths for IsPathAllowed.
func FileURIToPath(uri string) string {
	if strings.HasPrefix(uri, "file:///") {
		return "/" + strings.TrimPrefix(uri, "file:///")
	}
	if strings.HasPrefix(uri, "file://") {
		return strings.TrimPrefix(uri, "file://")
	}
	return uri
}
