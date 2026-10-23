// Package relaytest runs a real massrelay relay for tests: its HTTP app with
// JWT auth required, and the CollabService behind it for embedded joins.
package relaytest

import (
	"context"
	"net/http/httptest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/massrelay/services"
	relaymw "github.com/panyam/massrelay/web/middleware"
	relayserver "github.com/panyam/massrelay/web/server"
	"github.com/panyam/oneauth/keys"
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
}

// Start runs a relay that requires a relay token on every WebSocket, with
// the per-IP connection rate limit off, since test peers all dial from
// loopback.
func Start() (*Relay, error) {
	ks := keys.NewInMemoryKeyStore()
	if _, err := ks.PutKey(context.Background(), &keys.PutKeyRequest{Record: &keys.KeyRecord{
		ClientID: clientID, Key: []byte(secret), Algorithm: "HS256",
	}}); err != nil {
		return nil, err
	}
	auth := relaymw.NewRelayAuthenticator(relaymw.RelayAuthConfig{
		KeyStore: ks, Required: true, Issuer: issuer, TokenQueryParam: "token",
	})
	app := relayserver.NewRelayApp()
	app.Guard = relaymw.NewGuard(nil, nil, auth, nil, nil)
	if err := app.Init(); err != nil {
		return nil, err
	}
	ts := httptest.NewServer(app)
	return &Relay{Service: app.Service, URL: ts.URL, ts: ts}, nil
}

// Close stops the relay.
func (r *Relay) Close() { r.ts.Close() }

// ClientID is the client_id relay tokens carry, for embedded Principals.
const ClientID = clientID

// Token mints a relay token for subject, as an app would after
// authenticating its user. extra adds claims such as "rooms".
func Token(subject string, extra map[string]any) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": subject, "client_id": clientID, "type": "access", "iss": issuer,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}
