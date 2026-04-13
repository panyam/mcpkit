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
func IsPathAllowed(ctx context.Context, path string) bool {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.allowedRoots == nil {
		return true
	}
	roots := sc.allowedRoots()
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
func AllowedRoots(ctx context.Context) []string {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.allowedRoots == nil {
		return nil
	}
	return sc.allowedRoots()
}

// SetAllowedRoots installs an allowed-roots supplier on the session
// stored in ctx. Exported for the server dispatch layer to wire in
// the computed root set during session setup. Must be called AFTER
// ContextWithSession.
func SetAllowedRoots(ctx context.Context, fn func() []string) context.Context {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.allowedRoots = fn
	}
	return ctx
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
