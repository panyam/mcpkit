package massrelay

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	relaymw "github.com/panyam/massrelay/web/middleware"
	"github.com/panyam/oneauth/keys"
)

// Signer defaults. DefaultTokenTTL is short because a page refreshes its token
// over the open connection before it expires.
const (
	DefaultSignerClientID = "mcpkit-rooms"
	DefaultSignerIssuer   = "mcpkit-rooms"
	DefaultTokenTTL       = 10 * time.Minute
)

// SignerOptions configure NewSigner. Every field is optional.
type SignerOptions struct {
	// ClientID is the token's client_id. The relay looks up the signing key
	// by it. Default DefaultSignerClientID.
	ClientID string
	// Issuer is the token's iss, which the relay requires. Default
	// DefaultSignerIssuer.
	Issuer string
	// Key is the HS256 key. Default is 32 random bytes generated for this
	// process, so tokens stop working when it restarts and pages join again.
	// Pass a key to keep tokens valid across restarts or replicas.
	Key []byte
	// TTL is how long a minted token lasts. Default DefaultTokenTTL.
	TTL time.Duration
}

// Signer mints relay tokens and builds the authenticator that accepts them.
// It implements rooms.TokenMinter.
//
// massrelay#78 tracks moving the signing into massrelay itself, beside
// RelayAuthenticator. Once that is released this type delegates to it.
type Signer struct {
	clientID string
	issuer   string
	key      []byte
	ttl      time.Duration
	auth     *relaymw.RelayAuthenticator
}

// NewSigner returns a Signer, generating a key when o.Key is empty.
func NewSigner(o SignerOptions) (*Signer, error) {
	s := &Signer{clientID: o.ClientID, issuer: o.Issuer, key: append([]byte{}, o.Key...), ttl: o.TTL}
	if s.clientID == "" {
		s.clientID = DefaultSignerClientID
	}
	if s.issuer == "" {
		s.issuer = DefaultSignerIssuer
	}
	if s.ttl <= 0 {
		s.ttl = DefaultTokenTTL
	}
	if len(s.key) == 0 {
		s.key = make([]byte, 32)
		if _, err := rand.Read(s.key); err != nil {
			return nil, fmt.Errorf("massrelay: NewSigner: generating a key: %w", err)
		}
	}
	ks := keys.NewInMemoryKeyStore()
	if _, err := ks.PutKey(context.Background(), &keys.PutKeyRequest{Record: &keys.KeyRecord{
		ClientID: s.clientID, Key: s.key, Algorithm: "HS256",
	}}); err != nil {
		return nil, fmt.Errorf("massrelay: NewSigner: %w", err)
	}
	s.auth = relaymw.NewRelayAuthenticator(relaymw.RelayAuthConfig{
		KeyStore: ks, Required: true, Issuer: s.issuer, TokenQueryParam: "token",
	})
	return s, nil
}

// MintRoomToken returns a token for subject whose rooms claim names room
// alone, so the relay refuses it for any other room.
func (s *Signer) MintRoomToken(_ context.Context, subject, room string) (string, time.Time, error) {
	if subject == "" || room == "" {
		return "", time.Time{}, errors.New("massrelay: MintRoomToken: subject and room are required")
	}
	return s.mint(subject, []string{room})
}

func (s *Signer) mint(subject string, rooms []string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(s.ttl)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject, "client_id": s.clientID, "type": "access", "iss": s.issuer,
		"rooms": rooms, "iat": now.Unix(), "exp": exp.Unix(),
	}).SignedString(s.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("massrelay: minting a relay token: %w", err)
	}
	// The token's exp is whole seconds. Report that, not the finer time.
	return tok, time.Unix(exp.Unix(), 0).UTC(), nil
}

// Authenticator returns the relay authenticator that accepts this signer's
// tokens and requires one on every connection. It also checks the token in a
// page's ReAuth, so a refreshed token passes the same checks.
func (s *Signer) Authenticator() *relaymw.RelayAuthenticator { return s.auth }

// ClientID is the client_id the signer's tokens carry, for an Embedded
// principal on the same relay.
func (s *Signer) ClientID() string { return s.clientID }
