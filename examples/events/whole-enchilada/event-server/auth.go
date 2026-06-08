// auth.go wires the event-server's introspection-mode authentication.
// The MultiRealmIntrospectionValidator below fans Validate / Claims calls
// out to N child IntrospectionValidators in parallel — one per realm
// the operator listed in OAUTH_INTROSPECTION_URLS — and accepts the token
// if any child validates it. Each child stamps Claims.Tenant from its
// realm-from-issuer mapper, so downstream consumers (events lib's
// resolvePrincipal, MatchFuncs filtering by tenant) see the right
// tenant value without doing string-prefix matching on issuer URLs.
//
// The wrapper is event-server-local for now; promoted to ext/auth once a
// second consumer needs the same fan-out pattern (per the "Defer general
// abstractions" rule in CLAUDE.md memories).
package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
)

// MultiRealmIntrospectionValidator wraps N IntrospectionValidators
// (one per realm) and accepts a request if ANY child accepts it.
// Implements core.AuthValidator and core.ClaimsProvider so the server
// can use it interchangeably with a single-realm IntrospectionValidator.
//
// Validation strategy: short-circuit on first-success. Each child is
// tried in turn; the first one that returns nil from Validate wins,
// and its Claims are what Claims(r) returns. Children that reject the
// token (inactive, wrong issuer, etc.) move to the next; only when ALL
// children reject does the wrapper return an AuthError.
//
// Concurrency: Validate iterates children serially (each
// introspection call is a HTTP round trip; in-flight parallelism per
// request is rarely worth the latency-vs-cost trade for typical realm
// counts of 2-5). When N grows large enough that latency matters,
// switch to parallel fan-out — the API surface stays the same.
type MultiRealmIntrospectionValidator struct {
	children []*auth.IntrospectionValidator

	// validResults caches "which child accepted this token" so Claims(r)
	// can route to the right child's Claims handoff without retrying
	// every child. Keyed by raw bearer token; entries are popped by
	// Claims, matching the per-request one-shot pattern from
	// JWTValidator / IntrospectionValidator.
	mu            sync.Mutex
	validResults  map[string]*auth.IntrospectionValidator
}

// NewMultiRealmIntrospectionValidator builds a wrapper over the given
// children. At least one child is required — calling with zero
// children produces a wrapper that rejects every request.
func NewMultiRealmIntrospectionValidator(children []*auth.IntrospectionValidator) *MultiRealmIntrospectionValidator {
	return &MultiRealmIntrospectionValidator{
		children:     children,
		validResults: make(map[string]*auth.IntrospectionValidator),
	}
}

// Validate implements core.AuthValidator. Returns nil iff at least one
// child accepts the token; otherwise returns the last child's
// AuthError (any child's 401 / 403 is representative — the AS-specific
// error string changes nothing since none accepted us).
func (m *MultiRealmIntrospectionValidator) Validate(r *http.Request) error {
	const prefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, prefix) {
		return &mcpcore.AuthError{
			Code:    http.StatusUnauthorized,
			Message: "missing or invalid Authorization header",
		}
	}
	token := authHeader[len(prefix):]

	var lastErr error
	for _, child := range m.children {
		if err := child.Validate(r); err != nil {
			lastErr = err
			continue
		}
		m.mu.Lock()
		m.validResults[token] = child
		m.mu.Unlock()
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return &mcpcore.AuthError{
		Code:    http.StatusUnauthorized,
		Message: "no realm accepted the bearer token",
	}
}

// Claims implements core.ClaimsProvider by routing to whichever child
// accepted the token during the matching Validate call. Returns nil
// when no Validate has succeeded for this token in the current
// per-request window (matches single-realm IntrospectionValidator's
// one-shot LoadAndDelete semantics).
func (m *MultiRealmIntrospectionValidator) Claims(r *http.Request) *mcpcore.Claims {
	const prefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, prefix) {
		return nil
	}
	token := authHeader[len(prefix):]
	m.mu.Lock()
	child, ok := m.validResults[token]
	if ok {
		delete(m.validResults, token)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return child.Claims(r)
}

// realmConfig captures the env-driven configuration the multi-realm
// validator wraps: a list of introspection URLs (typically the
// .well-known endpoints of each realm) plus shared client credentials.
// Each URL produces one child IntrospectionValidator.
type realmConfig struct {
	URLs         []string
	ClientID     string
	ClientSecret string
	CacheTTL     time.Duration
}

// buildMultiRealmValidator constructs the wrapper from a realmConfig.
// Returns nil when URLs is empty — the caller falls back to the next
// auth posture (JWT or anonymous) per the env-driven chain in main.go.
func buildMultiRealmValidator(cfg realmConfig) *MultiRealmIntrospectionValidator {
	if len(cfg.URLs) == 0 {
		return nil
	}
	children := make([]*auth.IntrospectionValidator, 0, len(cfg.URLs))
	for _, url := range cfg.URLs {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		children = append(children, auth.NewIntrospectionValidator(auth.IntrospectionConfig{
			IntrospectionURL: url,
			ClientID:         cfg.ClientID,
			ClientSecret:     cfg.ClientSecret,
			CacheTTL:         cfg.CacheTTL,
		}))
	}
	if len(children) == 0 {
		return nil
	}
	return NewMultiRealmIntrospectionValidator(children)
}
