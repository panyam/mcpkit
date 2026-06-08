package auth

import "strings"

// PrincipalTenantSeparator is the single character that separates the
// tenant prefix from the subject inside an encoded principal. The
// IntrospectionValidator and JWTValidator both stamp claims as
// "<tenant>" + PrincipalTenantSeparator + "<subject>" when the
// validator's TenantMapper returns a non-empty tenant; downstream code
// (hooks, admin tooling) uses TenantOf / SubjectOf to split it back.
//
// Pinned to "/" because it is unambiguous in OAuth subject identifiers
// (subjects are opaque strings; an embedded "/" would already break
// any tenancy-naive equality check upstream) and matches the natural
// Keycloak realm-URL slash layout that the default mapper consumes.
// Changing the separator is a wire-shape change — bump a major
// version, do not flip it in a patch.
const PrincipalTenantSeparator = "/"

// TenantOf returns the tenant prefix of an encoded principal, or "" if
// the principal carries no tenant prefix. Used by hook authors
// (MatchFunc, TransformFunc) that need to scope deliveries by tenant
// without re-implementing the parse rule.
//
// Examples:
//
//	TenantOf("tenant-a/alice")    == "tenant-a"
//	TenantOf("alice")             == ""        // single-tenant deployment
//	TenantOf("")                  == ""
//	TenantOf("/alice")            == ""        // leading separator → empty tenant
//	TenantOf("a/b/c")             == "a"       // splits on first separator only
//
// The rule is "everything before the first PrincipalTenantSeparator,
// returning empty when the separator is absent or leading." This pairs
// with SubjectOf so TenantOf(p) + sep + SubjectOf(p) reconstructs p
// only when TenantOf(p) is non-empty — single-tenant principals
// round-trip via SubjectOf alone.
func TenantOf(principal string) string {
	i := strings.IndexByte(principal, PrincipalTenantSeparator[0])
	if i <= 0 {
		return ""
	}
	return principal[:i]
}

// SubjectOf returns the subject portion of an encoded principal —
// everything after the first PrincipalTenantSeparator. When the
// principal has no separator (single-tenant deployment), SubjectOf
// returns the principal verbatim. Used in tandem with TenantOf when a
// caller wants both halves; pure subject-only callers can use it
// directly.
//
// Examples:
//
//	SubjectOf("tenant-a/alice")   == "alice"
//	SubjectOf("alice")            == "alice"   // no separator → no tenant
//	SubjectOf("")                 == ""
//	SubjectOf("/alice")           == "alice"   // leading separator → empty tenant
//	SubjectOf("a/b/c")            == "b/c"     // splits on first separator only
func SubjectOf(principal string) string {
	i := strings.IndexByte(principal, PrincipalTenantSeparator[0])
	if i < 0 {
		return principal
	}
	return principal[i+1:]
}
