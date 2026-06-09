// Package auth: OIDC Back-Channel Logout 1.0 receiver.
//
// Spec: https://openid.net/specs/openid-connect-backchannel-1_0.html
//
// The receiver accepts the AS-initiated POST that fires when a session
// is revoked, validates the carried logout_token JWT, and invokes
// registered listeners with the (sub, sid) tuple that identifies which
// session ended. Application-level code (e.g., panyam/mcpkit's
// experimental/ext/events lib) hooks the listener to do the actual
// downstream work — kill webhook subscriptions, evict caches, log
// audit events.
//
// The handler is deliberately separate from JWTValidator and
// IntrospectionValidator: BCL logout_tokens have non-overlapping claim
// constraints with access tokens (events claim required, nonce
// forbidden, sub-or-sid one-of), and BCL semantically isn't request
// authentication — it's a server-to-server notification on its own
// URL. Sharing a validator with access tokens would just produce an
// option bag of "is this a logout_token? then enforce these extra
// rules".
//
// Mount example:
//
//	h, err := auth.NewBackChannelLogoutHandler(auth.BackChannelLogoutConfig{
//	    Issuer:   "http://keycloak.example.com/realms/tenant-a",
//	    Audience: "mcp-event-server",
//	    JWKSURL:  "http://keycloak.example.com/realms/tenant-a/protocol/openid-connect/certs",
//	})
//	if err != nil { ... }
//	h.RegisterListener(func(ctx context.Context, sub, sid string) {
//	    log.Printf("session ended: sub=%s sid=%s", sub, sid)
//	    // walk subscriptions, fire PostTerminated, etc.
//	})
//	mux.Handle("/backchannel-logout", h)
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/panyam/oneauth/keys"
	"github.com/panyam/oneauth/utils"
)

// BackChannelLogoutEventURI is the JWT `events` claim member that
// every OIDC Back-Channel Logout token MUST carry (spec § 2.4).
const BackChannelLogoutEventURI = "http://schemas.openid.net/event/backchannel-logout"

// BackChannelLogoutConfig configures the receiver. Required fields
// have a "required" callout; the rest pick defaults that match
// the spec's MUST window plus a small clock-skew leeway.
type BackChannelLogoutConfig struct {
	// Issuer is the expected `iss` claim value (the AS issuer URL).
	// Required. Mirrors the same iss the AS publishes on its
	// /.well-known/openid-configuration document.
	Issuer string

	// Audience is the expected `aud` claim value — typically the
	// resource server's client_id registered with the AS.
	// Required. The spec mandates aud validation against the
	// registered client_id (§ 2.6).
	Audience string

	// JWKSURL is the AS JWKS endpoint used to fetch the signing
	// key for the logout_token via its kid header. Required.
	// Wrapped in oneauth's JWKSKeyStore for cached lookups.
	JWKSURL string

	// JTIStore stores seen `jti` values for replay protection.
	// Default: a fresh MemoryJTIStore. Multi-replica deployments
	// should swap for a Redis / SQL backed impl so a replay seen
	// on replica A is also rejected on replica B.
	JTIStore JTIStore

	// ReplayWindow is the TTL applied to recorded jtis — how long
	// a jti remains visible to JTIStore.Seen. Default 10 minutes.
	// Set this to at least 2x the AS's max-clock-skew tolerance so
	// a delayed-replay attack can't slip past the eviction.
	ReplayWindow time.Duration

	// AllowedClockSkew is the leeway applied to exp / iat / nbf
	// checks during JWT validation. Default 60s. The spec is silent
	// on a concrete value; a minute is the common floor across OIDC
	// libraries.
	AllowedClockSkew time.Duration

	// Now returns the current time. Defaults to time.Now. Tests
	// inject a fixed clock so exp / iat / replay-window assertions
	// are deterministic.
	Now func() time.Time
}

// LogoutListener is invoked synchronously after a logout_token
// validates. sub is the OIDC subject claim (may be empty if the token
// only carried sid); sid is the OIDC session ID claim (may be empty
// if the token only carried sub). At least one is guaranteed
// non-empty by the spec-mandated sub-or-sid check in ServeHTTP.
//
// ctx is the request context; cancellation propagates through it.
// Listeners that do expensive work should spawn their own goroutine
// — the receiver does not impose a deadline, but slow listeners
// extend the AS-side BCL POST latency, which can lead to AS retries
// or alerts depending on the AS implementation.
type LogoutListener func(ctx context.Context, sub, sid string)

// BackChannelLogoutHandler implements http.Handler for the OIDC
// Back-Channel Logout 1.0 receiver endpoint. Construct via
// NewBackChannelLogoutHandler; mount on any http.ServeMux at the
// path the AS was configured to POST to.
type BackChannelLogoutHandler struct {
	cfg BackChannelLogoutConfig
	ks  *keys.JWKSKeyStore

	mu        sync.RWMutex
	listeners []LogoutListener
}

// NewBackChannelLogoutHandler constructs a receiver from cfg.
// Returns an error when a required field is missing or empty.
func NewBackChannelLogoutHandler(cfg BackChannelLogoutConfig) (*BackChannelLogoutHandler, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("BackChannelLogoutConfig.Issuer is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("BackChannelLogoutConfig.Audience is required")
	}
	if cfg.JWKSURL == "" {
		return nil, errors.New("BackChannelLogoutConfig.JWKSURL is required")
	}
	if cfg.JTIStore == nil {
		cfg.JTIStore = NewMemoryJTIStore()
	}
	if cfg.ReplayWindow <= 0 {
		cfg.ReplayWindow = 10 * time.Minute
	}
	if cfg.AllowedClockSkew <= 0 {
		cfg.AllowedClockSkew = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &BackChannelLogoutHandler{
		cfg: cfg,
		ks:  keys.NewJWKSKeyStore(cfg.JWKSURL),
	}, nil
}

// RegisterListener appends a listener invoked for every valid
// logout_token. Listeners run synchronously in registration order;
// short callbacks only, or hand off to a goroutine.
func (h *BackChannelLogoutHandler) RegisterListener(fn LogoutListener) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	h.listeners = append(h.listeners, fn)
	h.mu.Unlock()
}

// ServeHTTP implements the OIDC BCL POST endpoint per spec § 2.7.
// The handler returns:
//   - 200 OK with `Cache-Control: no-store` on successful validation
//   - 400 Bad Request with a categorical reason on any validation
//     failure
//   - 405 Method Not Allowed on non-POST methods
//
// The 400 response body is a JSON object {"error":"..."} where the
// error string identifies which check failed — useful for operators
// debugging an AS misconfiguration without leaking secrets.
func (h *BackChannelLogoutHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeBCLError(w, "bad form body: "+err.Error())
		return
	}
	token := r.PostForm.Get("logout_token")
	if token == "" {
		writeBCLError(w, "missing logout_token")
		return
	}

	sub, sid, err := h.validate(r.Context(), token)
	if err != nil {
		writeBCLError(w, err.Error())
		return
	}

	h.fire(r.Context(), sub, sid)
	w.WriteHeader(http.StatusOK)
}

// validate runs the full spec § 2.6 acceptance check on a single
// logout_token. Returns (sub, sid, nil) on success. Returns a
// descriptive error on any failure; the error string is what the
// 400 response body will carry, so it must be safe to surface to
// the operator.
func (h *BackChannelLogoutHandler) validate(ctx context.Context, token string) (sub, sid string, err error) {
	// WithoutClaimsValidation: jwt.Parse's built-in exp/iat/nbf checks
	// hit time.Now() directly, which would bypass our injectable
	// cfg.Now (used by tests for deterministic exp/iat assertions).
	// checkTimeClaims below runs the same checks against cfg.Now.
	parsed, err := jwt.Parse(token, h.keyFunc(),
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}),
		jwt.WithoutClaimsValidation(),
	)
	if err != nil {
		return "", "", fmt.Errorf("invalid logout_token: %w", err)
	}
	if !parsed.Valid {
		return "", "", errors.New("invalid logout_token")
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", errors.New("invalid logout_token claims shape")
	}

	now := h.cfg.Now()
	if err := checkTimeClaims(mc, now, h.cfg.AllowedClockSkew); err != nil {
		return "", "", err
	}
	if iss, _ := mc["iss"].(string); iss != h.cfg.Issuer {
		return "", "", fmt.Errorf("invalid iss: expected %q", h.cfg.Issuer)
	}
	if err := checkAudience(mc, h.cfg.Audience); err != nil {
		return "", "", err
	}
	if err := checkEventsClaim(mc); err != nil {
		return "", "", err
	}
	if _, hasNonce := mc["nonce"]; hasNonce {
		// Spec § 2.4: "A nonce Claim MUST NOT be present" — its
		// presence is one of the signature-forgery red flags a
		// well-meaning AS could be tricked into.
		return "", "", errors.New("nonce claim must not be present in logout_token")
	}

	sub, _ = mc["sub"].(string)
	sid, _ = mc["sid"].(string)
	if sub == "" && sid == "" {
		return "", "", errors.New("logout_token must carry at least one of sub or sid")
	}

	jti, _ := mc["jti"].(string)
	if jti == "" {
		// Spec § 2.4: jti is REQUIRED.
		return "", "", errors.New("missing required jti claim")
	}
	seen, err := h.cfg.JTIStore.Seen(ctx, SeenRequest{JTI: jti})
	if err != nil {
		return "", "", fmt.Errorf("jti store lookup failed: %w", err)
	}
	if seen.Found {
		return "", "", errors.New("logout_token jti has been seen before (replay)")
	}
	if _, err := h.cfg.JTIStore.Record(ctx, RecordRequest{JTI: jti, TTL: h.cfg.ReplayWindow}); err != nil {
		return "", "", fmt.Errorf("jti store record failed: %w", err)
	}
	return sub, sid, nil
}

// keyFunc returns a jwt.Keyfunc that resolves the verification key
// from the configured JWKS. Mirrors JWTValidator.jwksKeyFuncCtx —
// keyed on the token's `kid` header.
func (h *BackChannelLogoutHandler) keyFunc() jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("missing kid header")
		}
		rec, err := h.ks.GetKeyByKid(context.Background(), &keys.GetKeyByKidRequest{Kid: kid})
		if err != nil {
			return nil, fmt.Errorf("key not found for kid %q: %w", kid, err)
		}
		if rec == nil || rec.Record == nil {
			return nil, fmt.Errorf("key not found for kid %q", kid)
		}
		alg, _ := token.Header["alg"].(string)
		if alg != rec.Record.Algorithm {
			return nil, fmt.Errorf("algorithm mismatch: token has %s, key expects %s", alg, rec.Record.Algorithm)
		}
		return utils.DecodeVerifyKey(rec.Record.Key, rec.Record.Algorithm)
	}
}

// fire dispatches the validated logout to each registered listener
// in registration order. Listeners run synchronously; the handler
// completes its 200 response only after all listeners return.
func (h *BackChannelLogoutHandler) fire(ctx context.Context, sub, sid string) {
	h.mu.RLock()
	listeners := append([]LogoutListener(nil), h.listeners...)
	h.mu.RUnlock()
	for _, fn := range listeners {
		fn(ctx, sub, sid)
	}
}

// checkTimeClaims enforces exp/iat presence + freshness with the
// configured clock-skew leeway. iat is allowed to be slightly in the
// future to tolerate clock drift between the AS and the receiver.
func checkTimeClaims(mc jwt.MapClaims, now time.Time, skew time.Duration) error {
	exp, ok := toUnix(mc["exp"])
	if !ok {
		return errors.New("missing exp claim")
	}
	if now.After(exp.Add(skew)) {
		return errors.New("logout_token expired")
	}
	iat, ok := toUnix(mc["iat"])
	if !ok {
		return errors.New("missing iat claim")
	}
	if iat.After(now.Add(skew)) {
		return errors.New("logout_token issued in the future")
	}
	return nil
}

// checkAudience accepts either a string or []any audience claim and
// returns nil iff the configured Audience appears as one of the
// values. Spec § 2.6: the receiver MUST validate aud.
func checkAudience(mc jwt.MapClaims, want string) error {
	switch aud := mc["aud"].(type) {
	case string:
		if aud != want {
			return fmt.Errorf("invalid aud: expected %q, got %q", want, aud)
		}
		return nil
	case []any:
		for _, a := range aud {
			if s, _ := a.(string); s == want {
				return nil
			}
		}
		return fmt.Errorf("invalid aud: expected %q to appear in audience list", want)
	default:
		return errors.New("missing aud claim")
	}
}

// checkEventsClaim enforces spec § 2.4: the `events` claim is a JSON
// object whose members declare the logout-style event types. The BCL
// member URI MUST be present (its value is an empty object per the
// spec; we don't inspect the inner shape because future SETs may add
// fields).
func checkEventsClaim(mc jwt.MapClaims) error {
	raw, ok := mc["events"]
	if !ok {
		return errors.New("missing events claim")
	}
	events, ok := raw.(map[string]any)
	if !ok {
		return errors.New("events claim is not a JSON object")
	}
	if _, hasBCL := events[BackChannelLogoutEventURI]; !hasBCL {
		return fmt.Errorf("events claim missing %q member", BackChannelLogoutEventURI)
	}
	return nil
}

// toUnix coerces a jwt.MapClaims numeric claim (which json.Unmarshal
// represents as float64 by default) to a time.Time. Returns ok=false
// if the value is missing or not numeric.
func toUnix(v any) (time.Time, bool) {
	switch n := v.(type) {
	case float64:
		return time.Unix(int64(n), 0), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(i, 0), true
	case int64:
		return time.Unix(n, 0), true
	case int:
		return time.Unix(int64(n), 0), true
	default:
		return time.Time{}, false
	}
}

// writeBCLError writes a 400 response carrying the categorical
// reason. JSON shape mirrors how the introspection-error path formats
// failures elsewhere in ext/auth so operators see a consistent
// debugging surface.
func writeBCLError(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
}
