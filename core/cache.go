package core

// SEP-2549 cache-scope values for the cacheScope field carried on the
// cacheable result types: ToolsListResult, PromptsListResult,
// ResourcesListResult, ResourceTemplatesListResult, and ResourceResult
// (resources/read). cacheScope mirrors HTTP Cache-Control: public vs
// private and controls who may serve a cached copy of a response.
//
//   - CacheScopePublic — the response holds no caller-specific data. Any
//     client, shared gateway, or caching proxy MAY store the response and
//     serve it to any user. Appropriate for tool, prompt, and resource
//     template lists that are identical for every caller.
//   - CacheScopePrivate — the response holds caller-specific data. A cache
//     MAY be reused only within the same authorization context and MUST NOT
//     be shared across access tokens. Appropriate for resources/read results
//     that depend on the authenticated user, or for per-user filtered lists.
//
// When cacheScope is absent clients default to "public", so a server whose
// response varies per caller MUST set CacheScopePrivate explicitly.
// See docs/LIST_TTL_MIGRATION.md for the security rationale.
const (
	CacheScopePublic  = "public"
	CacheScopePrivate = "private"
)
