package auth

import (
	"context"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/keys"
	"github.com/panyam/oneauth/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakeSpan + fakeTracerProvider mirror server/trace_middleware_test.go's
// shape so the JWT-validator instrumentation tests can assert span
// emission and attribute placement without pulling in the OTel SDK
// (ext/auth's whole point is depending on the core abstraction only).
type fakeSpan struct {
	name   string
	parent mcpcore.Span
	attrs  map[string]string
	errors []error
	ended  bool
}

func (s *fakeSpan) End()                     { s.ended = true }
func (s *fakeSpan) SetAttribute(k, v string) { s.attrs[k] = v }
func (s *fakeSpan) RecordError(err error)    { s.errors = append(s.errors, err) }
func (s *fakeSpan) AddLink(mcpcore.Link)     {}

type fakeTracerProvider struct {
	mu    sync.Mutex
	spans []*fakeSpan
}

func (p *fakeTracerProvider) StartSpan(ctx context.Context, name string, attrs ...mcpcore.Attribute) (context.Context, mcpcore.Span) {
	parent := mcpcore.SpanFromContext(ctx)
	sp := &fakeSpan{
		name:   name,
		parent: parent,
		attrs:  make(map[string]string, len(attrs)),
	}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.mu.Lock()
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	return mcpcore.WithActiveSpan(ctx, sp), sp
}

func (p *fakeTracerProvider) snapshot() []*fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*fakeSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

func (p *fakeTracerProvider) findSpan(name string) *fakeSpan {
	for _, sp := range p.snapshot() {
		if sp.name == name {
			return sp
		}
	}
	return nil
}

// requestWithActiveSpan returns an *http.Request whose ctx carries the
// supplied active span — simulates the position the SEP-414 P2 trace
// middleware leaves the dispatch span in by the time auth runs.
func requestWithActiveSpan(token string, span mcpcore.Span) *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if span != nil {
		r = r.WithContext(mcpcore.WithActiveSpan(r.Context(), span))
	}
	return r
}

// seedCachedClaims primes the validator's token cache so subsequent
// Validate(token) takes the cache-hit fast path — exercises the
// active-span attribute branch without needing real JWKS/RSA wiring.
func seedCachedClaims(v *JWTValidator, token string, claims *mcpcore.Claims) {
	v.tokenCache.Store(hashToken(token), &cachedClaims{
		claims: claims,
		expiry: time.Now().Add(time.Hour),
	})
}

func TestJWTValidator_Trace_NilTracerProvider_NoSpans(t *testing.T) {
	v := NewJWTValidator(JWTConfig{
		Issuer:   "https://test.example.com",
		Audience: "https://mcp.test",
		// TracerProvider intentionally omitted — must default to Noop.
	})
	v.CacheTTL = time.Minute
	seedCachedClaims(v, "cached.token.value", &mcpcore.Claims{Subject: "u1"})

	// No active span on the request ctx — exercises the "no parent
	// to decorate" branch in stampClaimsAttrs.
	r := requestWithActiveSpan("cached.token.value", nil)
	require.NoError(t, v.Validate(r))
	// No assertion possible beyond "didn't panic" — the test's value
	// is the safety contract: nil TracerProvider must not crash.
}

func TestJWTValidator_Trace_NoopTracerProvider_NoSpans(t *testing.T) {
	v := NewJWTValidator(JWTConfig{
		Issuer:         "https://test.example.com",
		Audience:       "https://mcp.test",
		TracerProvider: mcpcore.NoopTracerProvider{},
	})
	v.CacheTTL = time.Minute
	seedCachedClaims(v, "cached.token.value", &mcpcore.Claims{Subject: "u1"})

	r := requestWithActiveSpan("cached.token.value", nil)
	require.NoError(t, v.Validate(r))
	// Noop path produces no observable spans — the test's value is
	// confirming the Noop wiring doesn't accidentally fall through to
	// nil-deref behavior the nil-TP test above doesn't cover.
}

func TestJWTValidator_Trace_CacheHit_StampsActiveSpanAttributes(t *testing.T) {
	tp := &fakeTracerProvider{}
	v := NewJWTValidator(JWTConfig{
		Issuer:         "https://test.example.com",
		Audience:       "https://mcp.test",
		TracerProvider: tp,
	})
	v.CacheTTL = time.Minute

	cached := &mcpcore.Claims{
		Subject: "alice",
		Issuer:  "https://test.example.com",
		Scopes:  []string{"read", "write"},
	}
	seedCachedClaims(v, "cached.token.value", cached)

	// Start an outer "dispatch" span via the fake provider — mimics
	// the SEP-414 P2 trace middleware's outermost span. Validate must
	// stamp the mcp.auth.* attributes on THIS span, not a child.
	_, dispatchSpan := tp.StartSpan(context.Background(), "dispatch")
	r := requestWithActiveSpan("cached.token.value", dispatchSpan)
	require.NoError(t, v.Validate(r))

	parent := dispatchSpan.(*fakeSpan)
	assert.Equal(t, "jwt", parent.attrs["mcp.auth.method"],
		"mcp.auth.method must land on the active dispatch span")
	assert.Equal(t, "alice", parent.attrs["mcp.auth.subject"])
	assert.Equal(t, "https://test.example.com", parent.attrs["mcp.auth.issuer"])
	assert.Equal(t, "read,write", parent.attrs["mcp.auth.scopes"],
		"scopes encoded as comma-joined string (one searchable attr in Tempo)")
	assert.Equal(t, "true", parent.attrs["mcp.auth.cache_hit"],
		"cache-hit fast path must set cache_hit=true")

	for _, sp := range tp.snapshot() {
		assert.NotEqual(t, "auth.jwks_lookup", sp.name,
			"cache-hit path must NOT emit a jwks_lookup span — that's the whole point of the cache")
	}
}

func TestJWTValidator_Trace_FullValidate_EmitsJWKSLookupSpan(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKS(t, "alice", []string{"read"})
	defer cleanup()

	tp := &fakeTracerProvider{}
	v := NewJWTValidator(JWTConfig{
		JWKSURL:        jwksURL,
		Issuer:         issuer,
		Audience:       "https://mcp.test",
		TracerProvider: tp,
	})

	_, dispatchSpan := tp.StartSpan(context.Background(), "dispatch")
	r := requestWithActiveSpan(token, dispatchSpan)
	require.NoError(t, v.Validate(r))

	jwksSpan := tp.findSpan("auth.jwks_lookup")
	require.NotNil(t, jwksSpan, "full validate path must emit auth.jwks_lookup; spans were: %v", spanNames(tp.snapshot()))
	assert.NotEmpty(t, jwksSpan.attrs["mcp.auth.jwks.kid"],
		"jwks_lookup span must carry the kid being resolved")
	assert.True(t, jwksSpan.ended, "jwks_lookup span must be ended (defer would leave it open under panic)")
	assert.Same(t, dispatchSpan, jwksSpan.parent,
		"jwks_lookup must be a child of the active dispatch span, not a top-level orphan")

	parent := dispatchSpan.(*fakeSpan)
	assert.Equal(t, "false", parent.attrs["mcp.auth.cache_hit"],
		"first validate of an uncached token must set cache_hit=false")
	assert.Equal(t, "alice", parent.attrs["mcp.auth.subject"])
}

func TestJWTValidator_Trace_FullValidate_NoActiveSpan_StillEmitsJWKSChild(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKS(t, "alice", []string{"read"})
	defer cleanup()

	tp := &fakeTracerProvider{}
	v := NewJWTValidator(JWTConfig{
		JWKSURL:        jwksURL,
		Issuer:         issuer,
		Audience:       "https://mcp.test",
		TracerProvider: tp,
	})

	// No outer dispatch span — simulates auth running outside a traced
	// dispatch path (transport-level auth, or middleware ordered before
	// the trace middleware). The jwks_lookup span still emits; the
	// mcp.auth.* attributes go to a noop span and silently drop.
	r := requestWithActiveSpan(token, nil)
	require.NoError(t, v.Validate(r))

	jwksSpan := tp.findSpan("auth.jwks_lookup")
	require.NotNil(t, jwksSpan, "jwks_lookup must emit even without a dispatch-span parent")
	assert.True(t, jwksSpan.ended)
}

// setupTestJWKS spins up an httptest server hosting a JWKS endpoint
// backed by an oneauth in-memory key store, mints a signed JWT, and
// returns the JWKS URL + issuer + token + cleanup. Keeps the
// per-test fixture self-contained — examples/auth/common can't be
// imported (it depends on ext/auth — would be a cycle).
func setupTestJWKS(t *testing.T, subject string, scopes []string) (jwksURL, issuer, token string, cleanup func()) {
	return setupTestJWKSClaims(t, func(c jwt.MapClaims) {
		c["sub"] = subject
		c["scopes"] = scopes
	})
}

// setupTestJWKSClaims is setupTestJWKS with full control over the token's
// claim set (mutate receives the base claims — iss/aud/exp/iat prefilled — and
// adds sub/scope-shape/etc). Lets tests mint tokens in each IdP's scope claim
// format (scopes/scp array, scope/scp string) against a real JWKS.
func setupTestJWKSClaims(t *testing.T, mutate func(jwt.MapClaims)) (jwksURL, issuer, token string, cleanup func()) {
	t.Helper()

	privPEM, pubPEM, err := utils.GenerateRSAKeyPair(2048)
	require.NoError(t, err)

	parsed, err := utils.ParsePrivateKeyPEM(privPEM)
	require.NoError(t, err)
	privKey, ok := parsed.(*rsa.PrivateKey)
	require.True(t, ok, "expected RSA private key")

	const clientID = "test-key-1"
	ks := keys.NewInMemoryKeyStore()
	_, err = ks.PutKey(context.Background(), &keys.PutKeyRequest{
		Record: &keys.KeyRecord{
			ClientID:  clientID,
			Key:       pubPEM,
			Algorithm: "RS256",
		},
	})
	require.NoError(t, err)

	// KeyRecord.Kid is auto-computed from key material when PutKey
	// stores it — NOT the ClientID. Read it back so the JWT we mint
	// below carries a kid that matches what JWKSHandler publishes.
	stored, err := ks.GetKey(context.Background(), &keys.GetKeyRequest{ClientID: clientID})
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.Record)
	kid := stored.Record.Kid
	require.NotEmpty(t, kid, "oneauth must compute a kid for the stored key")

	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/jwks.json", &keys.JWKSHandler{KeyStore: ks})
	ts := httptest.NewServer(mux)

	jwksURL = ts.URL + "/.well-known/jwks.json"
	issuer = "https://test.example.com"

	claims := jwt.MapClaims{
		"iss": issuer,
		"aud": "https://mcp.test",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	mutate(claims)
	jwtTok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	jwtTok.Header["kid"] = kid
	token, err = jwtTok.SignedString(privKey)
	require.NoError(t, err)

	// Sanity — confirm the public PEM is what we expect oneauth to
	// publish. Catches a future PEM-format drift before the test
	// fails with an obscure JWT verification error.
	block, _ := pem.Decode(pubPEM)
	require.NotNil(t, block, "public key PEM must decode")
	require.True(t, strings.HasPrefix(block.Type, "PUBLIC") || strings.HasPrefix(block.Type, "RSA PUBLIC"),
		"unexpected PEM block type: %s", block.Type)

	cleanup = func() { ts.Close() }
	return
}

func spanNames(spans []*fakeSpan) string {
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.name)
	}
	return fmt.Sprintf("%v", names)
}

// TestJWTValidator_Trace_OneauthTracerProvider_EmitsOneauthSpans
// confirms that wiring JWTConfig.OneauthTracerProvider threads
// through to oneauth's keys.WithTracerProvider option, so
// oneauth-internal work (JWKS HTTP fetch, key cache lookup,
// signature verify on the keystore side) emits spans on the
// supplied OTel pipeline. The auth.jwks_lookup span ext/auth emits
// is mcpkit-side and gates this via TracerProvider (separate
// field, already tested above); the assertion here is specifically
// that oneauth-emitted span names land in the SDK exporter —
// proves the option flowed through.
func TestJWTValidator_Trace_OneauthTracerProvider_EmitsOneauthSpans(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKS(t, "alice", []string{"read"})
	defer cleanup()

	exp := tracetest.NewInMemoryExporter()
	sdkTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = sdkTP.Shutdown(context.Background()) })

	v := NewJWTValidator(JWTConfig{
		JWKSURL:               jwksURL,
		Issuer:                issuer,
		Audience:              "https://mcp.test",
		OneauthTracerProvider: sdkTP,
	})

	r := requestWithActiveSpan(token, nil)
	require.NoError(t, v.Validate(r))

	names := sdkSpanNames(exp.GetSpans())
	require.NotEmpty(t, names,
		"setting OneauthTracerProvider must result in at least one oneauth-emitted span in the SDK exporter; got none")

	// oneauth v0.1.14 emits "oneauth.jwks.refresh", "oneauth.jwks.key_lookup",
	// "oneauth.signature_verify", etc. We don't pin to a specific name
	// (oneauth owns its span vocabulary and may evolve it) — assert
	// at least one span starts with "oneauth." so a future rename
	// internal to oneauth doesn't break this test, but a regression
	// that disconnects the wiring still does.
	foundOneauthSpan := false
	for _, n := range names {
		if strings.HasPrefix(n, "oneauth.") {
			foundOneauthSpan = true
			break
		}
	}
	assert.True(t, foundOneauthSpan,
		"expected at least one oneauth.* span; got %v", names)
}

func sdkSpanNames(spans tracetest.SpanStubs) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Name)
	}
	return out
}
