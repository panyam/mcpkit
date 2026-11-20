package events

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/utils"
	webhooks "github.com/panyam/servicekit/http/webhooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identityPost struct {
	header http.Header
	body   []byte
}

type identityReceiver struct {
	mu    sync.Mutex
	posts []identityPost
}

func (c *identityReceiver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.posts = append(c.posts, identityPost{header: r.Header.Clone(), body: body})
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (c *identityReceiver) waitFor(t *testing.T, n int) []identityPost {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.posts) >= n
	}, 2*time.Second, 10*time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]identityPost(nil), c.posts...)
}

func newEd25519Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

const identitySecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// deliverOne registers a target on a fresh registry built with opts,
// delivers one event and one gap envelope, and returns both POSTs.
func deliverOne(t *testing.T, opts ...WebhookOption) (*WebhookRegistry, []identityPost) {
	t.Helper()
	rcv := &identityReceiver{}
	srv := httptest.NewServer(rcv.handler())
	t.Cleanup(srv.Close)
	r := NewWebhookRegistry(append([]WebhookOption{
		WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks(),
	}, opts...)...)
	canonical := canonicalKey("alice", srv.URL, "fake.event", nil)
	r.Register(RegisterParams{CanonicalKey: canonical, DerivedID: deriveSubscriptionID(canonical), URL: srv.URL, Secret: identitySecret})
	r.Deliver(context.Background(), MakeEvent("fake.event", "evt_1", "1", time.Now(), map[string]string{"k": "v"}))
	rcv.waitFor(t, 1)
	r.PostGap(canonical, "cursor-1")
	return r, rcv.waitFor(t, 2)
}

func verifyIdentity(set utils.JWKSet, p identityPost) (string, bool) {
	return VerifyServerIdentity(set, p.header.Get(webhooks.HeaderID), p.header.Get(webhooks.HeaderTimestamp), p.body, p.header.Get(webhooks.HeaderSignature))
}

func TestServerIdentity_DeliveriesCarryV1AndV1a(t *testing.T) {
	key := newEd25519Key(t)
	r, posts := deliverOne(t, WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: key}))
	set := r.SigningJWKS()

	for i, p := range posts {
		sig := p.header.Get(webhooks.HeaderSignature)
		entries := strings.Fields(sig)
		require.Len(t, entries, 2, "post %d: want the v1 HMAC then one v1a, got %q", i, sig)
		assert.True(t, strings.HasPrefix(entries[0], "v1,"), "post %d: v1 first", i)
		assert.True(t, strings.HasPrefix(entries[1], "v1a,"), "post %d: then v1a", i)
		assert.True(t, VerifyStandardWebhooksSignature(p.body, identitySecret, p.header.Get(webhooks.HeaderID), p.header.Get(webhooks.HeaderTimestamp), sig),
			"post %d: the v1 HMAC still verifies with v1a beside it", i)
		kid, ok := verifyIdentity(set, p)
		assert.True(t, ok, "post %d: v1a must verify against the published JWKS", i)
		assert.Equal(t, "k1", kid, "post %d", i)
	}
}

func TestServerIdentity_VerificationPOSTIsSigned(t *testing.T) {
	key := newEd25519Key(t)
	rcv := newVerificationReceiver(t, nil)
	srv, reg := buildVerifyingStack(t, "alice", WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: key}))
	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil))
	require.Nil(t, resp.Error, "%+v", resp.Error)

	first := rcv.snapshot()[0]
	require.Equal(t, "verification", first.json["type"])
	kid, ok := VerifyServerIdentity(reg.SigningJWKS(), first.header.Get("webhook-id"), first.header.Get("webhook-timestamp"), first.body, first.header.Get("webhook-signature"))
	assert.True(t, ok, "the verification POST must carry a v1a signature too")
	assert.Equal(t, "k1", kid)
}

func TestServerIdentity_RotationSignsWithEveryKey(t *testing.T) {
	oldKey, newKey := newEd25519Key(t), newEd25519Key(t)
	r, posts := deliverOne(t, WithWebhookSigningKeys(
		SigningKey{KID: "old", Signer: oldKey},
		SigningKey{KID: "new", Signer: newKey},
	))
	set := r.SigningJWKS()
	require.Len(t, set.Keys, 2, "both keys are published during rotation")

	p := posts[0]
	assert.Len(t, strings.Fields(p.header.Get(webhooks.HeaderSignature)), 3, "v1 plus one v1a per key")
	for _, kid := range []string{"old", "new"} {
		only := utils.JWKSet{}
		for _, k := range set.Keys {
			if k.Kid == kid {
				only.Keys = append(only.Keys, k)
			}
		}
		got, ok := verifyIdentity(only, p)
		assert.True(t, ok, "a gateway that trusts only %q still verifies", kid)
		assert.Equal(t, kid, got)
	}
}

func TestServerIdentity_UntrustedOrTamperedFails(t *testing.T) {
	key := newEd25519Key(t)
	_, posts := deliverOne(t, WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: key}))
	p := posts[0]

	other := NewWebhookRegistry(WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: newEd25519Key(t)}))
	_, ok := verifyIdentity(other.SigningJWKS(), p)
	assert.False(t, ok, "another server's key must not verify, even with the same kid")

	r := NewWebhookRegistry(WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: key}))
	tampered := identityPost{header: p.header, body: append(bytes.Clone(p.body), ' ')}
	_, ok = verifyIdentity(r.SigningJWKS(), tampered)
	assert.False(t, ok, "a changed body must not verify")

	_, ok = verifyIdentity(utils.JWKSet{}, p)
	assert.False(t, ok, "an empty key set verifies nothing")
}

func TestServerIdentity_NoKeysMeansNoV1a(t *testing.T) {
	r, posts := deliverOne(t)
	assert.Empty(t, r.SigningJWKS().Keys)
	assert.Len(t, strings.Fields(posts[0].header.Get(webhooks.HeaderSignature)), 1, "only the v1 HMAC without signing keys")
}

func TestServerIdentity_MCPHeadersModeSendsNoV1aAndWarns(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	_, posts := deliverOne(t, WithWebhookHeaderMode(MCPHeaders), WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: newEd25519Key(t)}))
	assert.Empty(t, posts[0].header.Get(webhooks.HeaderSignature), "MCPHeaders has no webhook-signature header")
	assert.NotEmpty(t, posts[0].header.Get("X-MCP-Signature"))
	assert.Contains(t, buf.String(), "WithWebhookSigningKeys has no effect in mcp header mode")
}

type failingSigner struct{ pub ed25519.PublicKey }

func (f failingSigner) Public() crypto.PublicKey { return f.pub }
func (failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("kms unavailable")
}

func TestServerIdentity_FailingSignerKeepsDelivering(t *testing.T) {
	good := newEd25519Key(t)
	bad := failingSigner{pub: newEd25519Key(t).Public().(ed25519.PublicKey)}
	r, posts := deliverOne(t, WithWebhookSigningKeys(
		SigningKey{KID: "bad", Signer: bad},
		SigningKey{KID: "good", Signer: good},
	))
	p := posts[0]
	assert.Len(t, strings.Fields(p.header.Get(webhooks.HeaderSignature)), 2, "v1 plus the good key's v1a; the failed key is left out")
	kid, ok := verifyIdentity(r.SigningJWKS(), p)
	assert.True(t, ok)
	assert.Equal(t, "good", kid)
}

func TestSigningJWKS_Shape(t *testing.T) {
	key := newEd25519Key(t)
	r := NewWebhookRegistry(WithWebhookSigningKeys(SigningKey{Signer: key}))
	raw, err := json.Marshal(r.SigningJWKS())
	require.NoError(t, err)

	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Len(t, doc.Keys, 1)
	k := doc.Keys[0]
	assert.Equal(t, "OKP", k["kty"])
	assert.Equal(t, "Ed25519", k["crv"])
	assert.Equal(t, "EdDSA", k["alg"])
	assert.Equal(t, "sig", k["use"])
	assert.NotContains(t, k, "d", "the private key must never be published")

	thumb, err := utils.ComputeKid(key.Public().(ed25519.PublicKey), SigningJWKAlg)
	require.NoError(t, err)
	assert.Equal(t, thumb, k["kid"], "an empty KID defaults to the RFC 8037 thumbprint")
}

func TestVerifyServerIdentity_SkipsNonEd25519Keys(t *testing.T) {
	key := newEd25519Key(t)
	_, posts := deliverOne(t, WithWebhookSigningKeys(SigningKey{KID: "ed", Signer: key}))
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	set := utils.JWKSet{Keys: []utils.JWK{
		utils.ECDSAPublicKeyToJWK("ec", "ES256", &ec.PublicKey),
		{Kty: "OKP", Crv: "X25519", Kid: "x", X: "AAAA"},
		utils.Ed25519PublicKeyToJWK("ed", SigningJWKAlg, key.Public().(ed25519.PublicKey)),
	}}
	kid, ok := verifyIdentity(set, posts[0])
	assert.True(t, ok)
	assert.Equal(t, "ed", kid)
}

func TestWithWebhookSigningKeys_PanicsOnBadConfig(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key := newEd25519Key(t)
	cases := map[string][]SigningKey{
		"nil signer":    {{KID: "a"}},
		"not ed25519":   {{KID: "a", Signer: ec}},
		"duplicate kid": {{KID: "a", Signer: key}, {KID: "a", Signer: newEd25519Key(t)}},
	}
	for name, keys := range cases {
		func() {
			defer func() {
				msg, _ := recover().(string)
				assert.Contains(t, msg, "events: WithWebhookSigningKeys", name)
			}()
			WithWebhookSigningKeys(keys...)
		}()
	}
}

// cardFor serves a server with a Server Card and events registered on reg,
// and returns the card as a client fetches it.
func cardFor(t *testing.T, reg *WebhookRegistry) (*core.ServerCard, string) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "events-card", Version: "1.0.0"},
		server.WithServerCard(server.WithCardName("io.github.example/events-card"), server.WithCardDescription("test")))
	Register(Config{Server: srv, Webhooks: reg, UnsafeAnonymousPrincipal: "alice"})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	mcpURL := ts.URL + "/mcp"
	card, err := client.FetchServerCard(context.Background(), mcpURL, client.WithInsecureCardFetch())
	require.NoError(t, err)
	return card, mcpURL
}

func TestServerCard_PublishesSigningKeysThatVerifyDeliveries(t *testing.T) {
	key := newEd25519Key(t)
	opts := []WebhookOption{
		WithWebhookSigningKeys(SigningKey{KID: "k1", Signer: key}),
		WithWebhookEgressIPRanges("203.0.113.0/24", "2001:db8::/32"),
	}
	card, mcpURL := cardFor(t, NewWebhookRegistry(opts...))
	remote, ok := client.CardRemote(card, mcpURL)
	require.True(t, ok)
	info, ok := CardWebhooks(remote.ExtensionsInfo)
	require.True(t, ok, "the events entry must be on the card: %v", remote.ExtensionsInfo)
	require.NotNil(t, info.SigningKeys)
	require.Len(t, info.SigningKeys.Keys, 1)
	assert.Equal(t, []string{"203.0.113.0/24", "2001:db8::/32"}, info.EgressIPRanges)

	_, posts := deliverOne(t, opts...)
	kid, ok := verifyIdentity(*info.SigningKeys, posts[0])
	assert.True(t, ok, "a delivery must verify with the key taken from the card")
	assert.Equal(t, "k1", kid)
}

func TestServerCard_NoEventsEntryWithoutKeysOrRanges(t *testing.T) {
	card, mcpURL := cardFor(t, NewWebhookRegistry())
	remote, ok := client.CardRemote(card, mcpURL)
	require.True(t, ok)
	_, ok = CardWebhooks(remote.ExtensionsInfo)
	assert.False(t, ok, "nothing to publish, so no events entry: %v", remote.ExtensionsInfo)
}

func TestWithWebhookEgressIPRanges_PanicsOnBadCIDR(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		assert.Contains(t, msg, "events: WithWebhookEgressIPRanges")
	}()
	WithWebhookEgressIPRanges("203.0.113.0/24", "not-a-cidr")
	t.Fatal("expected a panic")
}
