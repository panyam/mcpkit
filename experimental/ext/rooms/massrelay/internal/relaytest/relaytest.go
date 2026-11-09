// Package relaytest runs a real massrelay relay for tests: its HTTP app with
// JWT auth required, and the CollabService behind it for embedded joins.
package relaytest

import (
	"net/http/httptest"
	"time"

	"github.com/panyam/massrelay/services"
	relaymw "github.com/panyam/massrelay/web/middleware"
	relayserver "github.com/panyam/massrelay/web/server"
)

const (
	secret   = "massrelay-adapter-test-secret"
	clientID = "rooms-test"
	issuer   = "rooms-test-issuer"
)

// Relay is a running relay. URL is its HTTP base; Service backs embedded
// joins.
type Relay struct {
	Service *services.CollabService
	URL     string
	ts      *httptest.Server
	app     *relayserver.RelayApp
}

// Start runs a relay that requires a relay token on every WebSocket, with
// the per-IP connection rate limit off, since test peers all dial from
// loopback.
func Start() (*Relay, error) {
	auth := newSigner().Authenticator(relaymw.RelayAuthConfig{Required: true, TokenQueryParam: "token"})
	app := relayserver.NewRelayApp()
	app.Guard = relaymw.NewGuard(nil, nil, auth, nil, nil)
	if err := app.Init(); err != nil {
		app.Close()
		return nil, err
	}
	ts := httptest.NewServer(app)
	return &Relay{Service: app.Service, URL: ts.URL, ts: ts, app: app}, nil
}

// Close stops the relay.
func (r *Relay) Close() {
	r.ts.Close()
	r.app.Close()
}

// ClientID is the client_id relay tokens carry, for embedded Principals.
const ClientID = clientID

// Issuer is the iss relay tokens carry. With ClientID and Key it builds a
// massrelay.Signer that a relay trusting this package's tokens also trusts.
const Issuer = issuer

// Key is the HS256 key relay tokens are signed with.
func Key() []byte { return []byte(secret) }

// Token mints a relay token for subject, as an app would after
// authenticating its user. With no rooms the token may join any room.
func Token(subject string, rooms ...string) (string, error) {
	tok, _, err := newSigner().Mint(subject, rooms)
	return tok, err
}

// newSigner is the signer behind every token the test relay accepts. The key
// is fixed, so each call returns an equivalent signer.
func newSigner() *relaymw.RelaySigner {
	s, err := relaymw.NewRelaySigner(relaymw.RelaySignerConfig{
		ClientID: clientID, Issuer: issuer, Key: []byte(secret), TTL: time.Hour,
	})
	if err != nil {
		panic(err)
	}
	return s
}
