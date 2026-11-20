package eventsclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	events "github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

var eventSeq atomic.Int64

const identitySecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

type identityData struct {
	N int `json:"n"`
}

func edKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signingRegistry(keys ...events.SigningKey) *events.WebhookRegistry {
	return events.NewWebhookRegistry(
		events.WithWebhookAllowPrivateNetworks(true),
		events.WithUnsafeWebhookAllowPlaintextCallbacks(),
		events.WithWebhookSigningKeys(keys...),
	)
}

// cardHandler serves the Server Card of a server whose events registry is
// reg. Swapping cur changes which server the card describes.
type cardHandler struct{ cur atomic.Pointer[http.Handler] }

func (c *cardHandler) set(reg *events.WebhookRegistry) {
	srv := server.NewServer(core.ServerInfo{Name: "identity", Version: "1.0.0"},
		server.WithServerCard(server.WithCardName("io.github.example/identity"), server.WithCardDescription("test")))
	events.Register(events.Config{Server: srv, Webhooks: reg, UnsafeAnonymousPrincipal: "alice"})
	h := srv.Handler(server.WithStreamableHTTP(true))
	c.cur.Store(&h)
}

func (c *cardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*c.cur.Load()).ServeHTTP(w, r)
}

func cardOrigin(t *testing.T, reg *events.WebhookRegistry) (*cardHandler, string) {
	t.Helper()
	ch := &cardHandler{}
	ch.set(reg)
	ts := httptest.NewServer(ch)
	t.Cleanup(ts.Close)
	return ch, ts.URL + "/mcp"
}

// deliverTo sends one event from reg to a receiver that checks identity
// against mcpURL's card, and reports whether the receiver accepted it.
func deliverTo(t *testing.T, reg *events.WebhookRegistry, rcv *Receiver[identityData]) bool {
	t.Helper()
	ts := httptest.NewServer(rcv)
	t.Cleanup(ts.Close)
	reg.Register(events.RegisterParams{CanonicalKey: []byte(t.Name()), DerivedID: "sub_identity", URL: ts.URL, Secret: identitySecret})
	reg.Deliver(context.Background(), events.MakeEvent("fake.event", fmt.Sprintf("evt_%d", eventSeq.Add(1)), "1", time.Now(), identityData{N: 1}))
	select {
	case ev := <-rcv.Events():
		return ev.Data.N == 1
	case <-time.After(1500 * time.Millisecond):
		return false
	}
}

func TestWithServerIdentity_AcceptsADeliverySignedWithTheCardsKey(t *testing.T) {
	reg := signingRegistry(events.SigningKey{KID: "k1", Signer: edKey(t)})
	_, mcpURL := cardOrigin(t, reg)
	rcv := NewReceiver[identityData](identitySecret, WithServerIdentity(mcpURL, client.WithInsecureCardFetch()))
	if !deliverTo(t, reg, rcv) {
		t.Fatal("a delivery signed with the card's key must be accepted")
	}
}

func TestWithServerIdentity_RefusesAnImpostorWithTheRightSecret(t *testing.T) {
	_, mcpURL := cardOrigin(t, signingRegistry(events.SigningKey{KID: "k1", Signer: edKey(t)}))
	impostor := signingRegistry(events.SigningKey{KID: "k1", Signer: edKey(t)})
	rcv := NewReceiver[identityData](identitySecret, WithServerIdentity(mcpURL, client.WithInsecureCardFetch()))
	if deliverTo(t, impostor, rcv) {
		t.Fatal("a delivery whose v1a key is not on the card must be refused, even with a valid HMAC")
	}
}

func TestWithServerIdentity_RefusesACardFromAnotherOrigin(t *testing.T) {
	reg := signingRegistry(events.SigningKey{KID: "k1", Signer: edKey(t)})
	_, realURL := cardOrigin(t, reg)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, realURL+"/server-card", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	rcv := NewReceiver[identityData](identitySecret, WithServerIdentity(redirector.URL+"/mcp", client.WithInsecureCardFetch()))
	if deliverTo(t, reg, rcv) {
		t.Fatal("keys from a card reached through a cross-origin redirect must not be used")
	}
}

func TestWithServerIdentity_RefetchesTheCardForARotatedKey(t *testing.T) {
	old := minCardRefetch
	minCardRefetch = 0
	t.Cleanup(func() { minCardRefetch = old })

	oldKey, newKey := events.SigningKey{KID: "old", Signer: edKey(t)}, events.SigningKey{KID: "new", Signer: edKey(t)}
	ch, mcpURL := cardOrigin(t, signingRegistry(oldKey))
	rcv := NewReceiver[identityData](identitySecret, WithServerIdentity(mcpURL, client.WithInsecureCardFetch()))
	if !deliverTo(t, signingRegistry(oldKey), rcv) {
		t.Fatal("setup: the old key must verify")
	}

	ch.set(signingRegistry(oldKey, newKey))
	if !deliverTo(t, signingRegistry(newKey), rcv) {
		t.Fatal("a delivery under a key the card added later must be accepted after one refetch")
	}
}

func TestWithServerIdentity_RefetchIsRateLimited(t *testing.T) {
	var fetches atomic.Int32
	reg := signingRegistry(events.SigningKey{KID: "k1", Signer: edKey(t)})
	ch, _ := cardOrigin(t, reg)
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		ch.ServeHTTP(w, r)
	}))
	t.Cleanup(counting.Close)

	rcv := NewReceiver[identityData](identitySecret, WithServerIdentity(counting.URL+"/mcp", client.WithInsecureCardFetch()))
	impostor := signingRegistry(events.SigningKey{KID: "x", Signer: edKey(t)})
	for i := 0; i < 3; i++ {
		deliverTo(t, impostor, rcv)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("card fetched %d times for 3 unverifiable deliveries inside the refetch interval, want 1", n)
	}
}
