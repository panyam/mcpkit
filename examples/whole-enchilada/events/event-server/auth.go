// auth.go wires the event-server's introspection-mode authentication.
// The IssRoutingIntrospectionValidator below indexes one child
// IntrospectionValidator per realm name (extracted from the
// introspection URL's /realms/<realm> segment) and routes each
// incoming request to the single child whose realm matches the
// token's `iss` claim — one introspection round trip per request,
// regardless of how many realms the operator listed.
//
// Why not fan-out across every realm: each token already advertises
// its issuer in the JWT body. Parsing the iss out (no signature
// verification — that still happens at introspection time) costs one
// base64 + json decode and saves N-1 round trips per request when N
// realms are configured. For N=3 that's a 67% cut; for larger N the
// ratio gets worse fast.
//
// Why iss is safe to trust for ROUTING: a spoofed iss only routes the
// request to a different realm's introspection endpoint, which then
// returns active=false and the request is rejected with 401 anyway.
// The introspection step IS the verification — iss is purely a
// dispatch hint.
//
// The wrapper is event-server-local for now; promoted to ext/auth
// once a second consumer needs the same routing pattern (per the
// "Defer general abstractions" rule in CLAUDE.md memories).
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
)

// IssRoutingIntrospectionValidator routes each bearer token to a
// single IntrospectionValidator picked by the realm name in the
// token's `iss` claim. Implements mcpcore.AuthValidator and
// mcpcore.ClaimsProvider so the server can use it interchangeably
// with a plain ext/auth IntrospectionValidator.
type IssRoutingIntrospectionValidator struct {
	// byRealm maps a Keycloak realm name to the IntrospectionValidator
	// pointed at THAT realm's introspection endpoint. Realm names come
	// from the URL's /realms/<realm>/ segment (see realmFromKeycloakURL)
	// and are matched against the realm parsed out of the inbound JWT's
	// iss claim.
	byRealm map[string]*auth.IntrospectionValidator

	// recentChild stashes "which child accepted this token" so the
	// subsequent Claims(r) lookup can route to the right child's
	// Claims handoff without re-deriving the realm. One-shot per
	// request — same LoadAndDelete pattern ext/auth's
	// IntrospectionValidator uses.
	mu          sync.Mutex
	recentChild map[string]*auth.IntrospectionValidator
}

// NewIssRoutingIntrospectionValidator builds a router over the given
// realm→child map. The map MUST contain at least one entry — an empty
// map produces a wrapper that rejects every request.
func NewIssRoutingIntrospectionValidator(byRealm map[string]*auth.IntrospectionValidator) *IssRoutingIntrospectionValidator {
	return &IssRoutingIntrospectionValidator{
		byRealm:     byRealm,
		recentChild: make(map[string]*auth.IntrospectionValidator),
	}
}

// Validate implements mcpcore.AuthValidator. Parses iss out of the
// bearer JWT (without verifying the signature), looks up the matching
// child, and delegates. Returns 401 if the header is missing/malformed,
// the token isn't a JWT, the iss has no /realms/ segment, or no child
// is registered for the derived realm.
func (v *IssRoutingIntrospectionValidator) Validate(r *http.Request) error {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return &mcpcore.AuthError{
			Code:    http.StatusUnauthorized,
			Message: "missing or invalid Authorization header",
		}
	}
	token := header[len(prefix):]
	realm, err := realmFromJWT(token)
	if err != nil {
		return &mcpcore.AuthError{
			Code:    http.StatusUnauthorized,
			Message: "could not route token: " + err.Error(),
		}
	}
	child, ok := v.byRealm[realm]
	if !ok {
		return &mcpcore.AuthError{
			Code:    http.StatusUnauthorized,
			Message: "no realm registered for token iss (realm=" + realm + ")",
		}
	}
	if err := child.Validate(r); err != nil {
		return err
	}
	v.mu.Lock()
	v.recentChild[token] = child
	v.mu.Unlock()
	return nil
}

// Claims implements mcpcore.ClaimsProvider by routing to whichever
// child accepted the token during the matching Validate call. Returns
// nil when no Validate has succeeded for this token in the current
// per-request window (matches ext/auth's one-shot LoadAndDelete
// semantics).
func (v *IssRoutingIntrospectionValidator) Claims(r *http.Request) *mcpcore.Claims {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return nil
	}
	token := header[len(prefix):]
	v.mu.Lock()
	child, ok := v.recentChild[token]
	if ok {
		delete(v.recentChild, token)
	}
	v.mu.Unlock()
	if !ok {
		return nil
	}
	return child.Claims(r)
}

// realmConfig captures the env-driven configuration the iss-routing
// validator wraps: a list of Keycloak introspection URLs plus shared
// client credentials. Each URL produces one keyed child.
type realmConfig struct {
	URLs         []string
	ClientID     string
	ClientSecret string
	CacheTTL     time.Duration
}

// buildIssRoutingValidator constructs the validator from a realmConfig.
// Returns nil when no recognizable Keycloak URLs remain after trim —
// the caller falls back to the next auth posture per main.go's chain.
func buildIssRoutingValidator(cfg realmConfig) *IssRoutingIntrospectionValidator {
	if len(cfg.URLs) == 0 {
		return nil
	}
	byRealm := make(map[string]*auth.IntrospectionValidator, len(cfg.URLs))
	for _, url := range cfg.URLs {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		realm := realmFromKeycloakURL(url)
		if realm == "" {
			// Non-Keycloak URL or no /realms/<r>/ segment — skip.
			continue
		}
		byRealm[realm] = auth.NewIntrospectionValidator(auth.IntrospectionConfig{
			IntrospectionURL: url,
			ClientID:         cfg.ClientID,
			ClientSecret:     cfg.ClientSecret,
			CacheTTL:         cfg.CacheTTL,
		})
	}
	if len(byRealm) == 0 {
		return nil
	}
	return NewIssRoutingIntrospectionValidator(byRealm)
}

// realmFromJWT extracts the Keycloak realm name from a JWT's iss claim
// without verifying the signature. Verification happens at
// introspection time — this is purely a routing hint, so iss spoofing
// just causes the request to be sent to the wrong realm's introspect
// endpoint, which returns active=false and the request is rejected.
func realmFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a JWT (expected 3 dot-separated parts)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("payload base64 decode failed: %w", err)
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("payload JSON decode failed: %w", err)
	}
	if claims.Iss == "" {
		return "", fmt.Errorf("token has no iss claim")
	}
	realm := realmFromKeycloakURL(claims.Iss)
	if realm == "" {
		return "", fmt.Errorf("iss=%q has no /realms/<r>/ segment", claims.Iss)
	}
	return realm, nil
}

// realmFromKeycloakURL extracts the realm name from any Keycloak URL
// that contains a /realms/<realm>/... segment — equally usable for
// introspection endpoints (.../realms/asgard/protocol/.../introspect)
// and bare issuer URLs (.../realms/asgard). Returns "" when the URL
// has no /realms/ segment.
func realmFromKeycloakURL(rawURL string) string {
	const marker = "/realms/"
	i := strings.LastIndex(rawURL, marker)
	if i < 0 {
		return ""
	}
	realm := rawURL[i+len(marker):]
	if j := strings.Index(realm, "/"); j >= 0 {
		realm = realm[:j]
	}
	return realm
}
