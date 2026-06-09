package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bclTestFixture spins up an httptest server hosting a JWKS document
// + the BackChannelLogoutHandler under test. Its `mint` helper builds
// a logout_token signed with the matching key — callers override
// claim fields case-by-case to exercise good + negative paths.
type bclTestFixture struct {
	t        *testing.T
	signKey  *rsa.PrivateKey
	kid      string
	asServer *httptest.Server
	rsServer *httptest.Server
	handler  *BackChannelLogoutHandler
	now      time.Time

	mu              sync.Mutex
	receivedSub     string
	receivedSid     string
	listenerInvoked bool
}

func newBCLFixture(t *testing.T) *bclTestFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fx := &bclTestFixture{
		t:       t,
		signKey: key,
		kid:     "test-kid-1",
		now:     time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
	}
	fx.asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Minimal RFC 7517 JWKS doc carrying the test public key.
		// oneauth's JWKSKeyStore looks up by kid via this document.
		n := key.PublicKey.N.Bytes()
		eBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(eBuf, uint64(key.PublicKey.E))
		// Strip leading zero bytes — JWKS wants the minimal big-endian form.
		eBytes := eBuf
		for len(eBytes) > 1 && eBytes[0] == 0 {
			eBytes = eBytes[1:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": fx.kid,
				"n":   base64.RawURLEncoding.EncodeToString(n),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			}},
		})
	}))
	t.Cleanup(fx.asServer.Close)

	h, err := NewBackChannelLogoutHandler(BackChannelLogoutConfig{
		Issuer:           "https://as.example.com",
		Audience:         "mcp-event-server",
		JWKSURL:          fx.asServer.URL + "/jwks",
		ReplayWindow:     10 * time.Minute,
		AllowedClockSkew: 60 * time.Second,
		Now:              func() time.Time { return fx.now },
	})
	require.NoError(t, err)
	h.RegisterListener(func(_ context.Context, sub, sid string) {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		fx.listenerInvoked = true
		fx.receivedSub = sub
		fx.receivedSid = sid
	})
	fx.handler = h
	fx.rsServer = httptest.NewServer(h)
	t.Cleanup(fx.rsServer.Close)
	return fx
}

// goodClaims returns a spec-compliant logout_token claim set for the
// fixture. Tests mutate the returned map to construct negative cases.
func (fx *bclTestFixture) goodClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": "https://as.example.com",
		"aud": "mcp-event-server",
		"iat": fx.now.Unix(),
		"exp": fx.now.Add(2 * time.Minute).Unix(),
		"jti": "jti-good-1",
		"sub": "user-abc",
		"sid": "sess-xyz",
		"events": map[string]any{
			BackChannelLogoutEventURI: map[string]any{},
		},
	}
}

// mint signs the given claims with the fixture's key and kid.
func (fx *bclTestFixture) mint(claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = fx.kid
	signed, err := tok.SignedString(fx.signKey)
	require.NoError(fx.t, err)
	return signed
}

// post fires the BCL POST to the handler with the given logout_token,
// returning status code + parsed JSON body.
func (fx *bclTestFixture) post(token string) (int, map[string]string) {
	body := url.Values{"logout_token": {token}}
	resp, err := http.Post(fx.rsServer.URL, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
	require.NoError(fx.t, err)
	defer resp.Body.Close()
	out := map[string]string{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestBCL_Good_FiresListener(t *testing.T) {
	fx := newBCLFixture(t)
	status, _ := fx.post(fx.mint(fx.goodClaims()))
	assert.Equal(t, http.StatusOK, status)
	fx.mu.Lock()
	defer fx.mu.Unlock()
	assert.True(t, fx.listenerInvoked, "listener must fire on valid logout_token")
	assert.Equal(t, "user-abc", fx.receivedSub)
	assert.Equal(t, "sess-xyz", fx.receivedSid)
}

func TestBCL_BadSignature_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	// Replace the signature with garbage.
	signed := fx.mint(fx.goodClaims())
	parts := strings.Split(signed, ".")
	parts[2] = "AAAA" + parts[2][4:]
	status, body := fx.post(strings.Join(parts, "."))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "invalid")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_WrongIssuer_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	cl := fx.goodClaims()
	cl["iss"] = "https://attacker.example.com"
	status, body := fx.post(fx.mint(cl))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "iss")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_MissingEventsClaim_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	cl := fx.goodClaims()
	delete(cl, "events")
	status, body := fx.post(fx.mint(cl))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "events")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_MissingSubAndSid_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	cl := fx.goodClaims()
	delete(cl, "sub")
	delete(cl, "sid")
	status, body := fx.post(fx.mint(cl))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "sub or sid")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_NoncePresent_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	cl := fx.goodClaims()
	cl["nonce"] = "abc"
	status, body := fx.post(fx.mint(cl))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "nonce")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_Expired_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	cl := fx.goodClaims()
	cl["exp"] = fx.now.Add(-5 * time.Minute).Unix() // well past clock-skew leeway
	status, body := fx.post(fx.mint(cl))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "expired")
	assert.False(t, fx.listenerInvoked)
}

func TestBCL_ReplayedJTI_Rejected(t *testing.T) {
	fx := newBCLFixture(t)
	token := fx.mint(fx.goodClaims())

	// First delivery: succeeds.
	status, _ := fx.post(token)
	require.Equal(t, http.StatusOK, status)
	fx.mu.Lock()
	require.True(t, fx.listenerInvoked, "first delivery must succeed")
	fx.listenerInvoked = false
	fx.mu.Unlock()

	// Same token, second delivery: must be rejected as replay.
	status, body := fx.post(token)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body["error"], "replay")
	fx.mu.Lock()
	defer fx.mu.Unlock()
	assert.False(t, fx.listenerInvoked, "replay must not fire the listener")
}

