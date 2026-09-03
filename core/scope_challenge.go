package core

import "context"

// ScopeChallenge is the authorization requirement a caller did not meet. A
// ScopeChallengeFunc returns one to refuse a request; the server turns it into
// an RFC 6750 §3.1 `403` carrying `WWW-Authenticate: Bearer
// error="insufficient_scope", scope="..."`.
type ScopeChallenge struct {
	// Scopes is the complete set the caller needs for this operation. It is
	// advertised verbatim in the challenge, so it is the server's answer to
	// "what should I ask my authorization server for". Must be non-empty;
	// an empty set is treated as a refusal with no remedy and fails closed.
	//
	// The spec (2025-11-25 Authorization, Server Scope Management) leaves the
	// inclusion strategy to the server and describes three: the newly required
	// scopes alone, those unioned with the caller's existing grants (which it
	// calls the recommended approach, since it stops clients dropping scopes
	// they already hold), or a wider related set. Any of the three is
	// conformant. Pick one and be consistent, which the spec also asks for.
	Scopes []string

	// ErrorDescription is an optional human-readable explanation, emitted as
	// the `error_description` challenge parameter. Keep it free of anything
	// the caller is not already authorized to know.
	ErrorDescription string
}

// ScopeChallengeFunc decides per request whether the caller needs more
// authorization. Returning (nil, nil) allows the request through.
//
// The callback sees the request, so the decision can depend on the arguments
// rather than only on which primitive was addressed. That distinction is the
// reason this exists: required scope is frequently a property of the call, not
// of the tool. GitHub's MCP server needs the `workflow` scope only when the
// paths in a commit touch `.github/workflows`, and needs repo-admin or
// org-admin depending on which target the arguments name. A declaration fixed
// at registration time cannot express either without over-asking on every
// call that does not need it.
//
// Claims are read from ctx via AuthClaims, HasScope and GetScopes, so the
// callback does not receive them separately.
//
// Failure is closed. A non-nil error refuses the request, and so does a
// challenge whose Scopes is empty.
type ScopeChallengeFunc func(ctx context.Context, req *Request) (*ScopeChallenge, error)

// RequireScopes builds a ScopeChallengeFunc that demands every scope in the
// list, which is the common case and the direct replacement for the deprecated
// RequiredScopes field:
//
//	srv.RegisterTool(core.ToolDef{
//	    Name:           "purge_notes",
//	    ScopeChallenge: core.RequireScopes("notes:write"),
//	}, handler)
//
// An unauthenticated caller is left alone here and refused by the server's
// authentication gate, which is the layer that owns the 401.
func RequireScopes(scopes ...string) ScopeChallengeFunc {
	required := append([]string(nil), scopes...)
	return func(ctx context.Context, _ *Request) (*ScopeChallenge, error) {
		if AuthClaims(ctx) == nil {
			return nil, nil
		}
		for _, s := range required {
			if !HasScope(ctx, s) {
				return &ScopeChallenge{Scopes: required}, nil
			}
		}
		return nil, nil
	}
}

// AcceptAnyScope builds a ScopeChallengeFunc satisfied by any one of the listed
// scopes, while advertising only the first as the remedy. It expresses a scope
// hierarchy without the server having to model one:
//
//	// docs:write is what we ask for; a caller holding the broader docs is fine
//	core.AcceptAnyScope("docs:write", "docs")
//
// Advertising only the first keeps the challenge least-privilege, so a caller
// holding neither is guided to the narrow scope rather than the broad one.
func AcceptAnyScope(advertise string, alternatives ...string) ScopeChallengeFunc {
	accepted := append([]string{advertise}, alternatives...)
	return func(ctx context.Context, _ *Request) (*ScopeChallenge, error) {
		if AuthClaims(ctx) == nil {
			return nil, nil
		}
		for _, s := range accepted {
			if HasScope(ctx, s) {
				return nil, nil
			}
		}
		return &ScopeChallenge{Scopes: []string{advertise}}, nil
	}
}
