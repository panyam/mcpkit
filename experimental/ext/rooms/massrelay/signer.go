package massrelay

import (
	"context"
	"errors"
	"fmt"
	"time"

	relaymw "github.com/panyam/massrelay/web/middleware"
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
// It implements rooms.TokenMinter on top of massrelay's RelaySigner, adding
// mcpkit's defaults and the one-room rule.
type Signer struct {
	rs   *relaymw.RelaySigner
	auth *relaymw.RelayAuthenticator
	id   string
}

// NewSigner returns a Signer, generating a key when o.Key is empty.
func NewSigner(o SignerOptions) (*Signer, error) {
	cfg := relaymw.RelaySignerConfig{ClientID: o.ClientID, Issuer: o.Issuer, TTL: o.TTL}
	if cfg.ClientID == "" {
		cfg.ClientID = DefaultSignerClientID
	}
	if cfg.Issuer == "" {
		cfg.Issuer = DefaultSignerIssuer
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTokenTTL
	}
	if len(o.Key) > 0 {
		cfg.Key = append([]byte{}, o.Key...)
	}
	rs, err := relaymw.NewRelaySigner(cfg)
	if err != nil {
		return nil, fmt.Errorf("massrelay: NewSigner: %w", err)
	}
	return &Signer{
		rs:   rs,
		auth: rs.Authenticator(relaymw.RelayAuthConfig{Required: true, TokenQueryParam: "token"}),
		id:   cfg.ClientID,
	}, nil
}

// MintRoomToken returns a token for subject whose rooms claim names room
// alone, so the relay refuses it for any other room.
func (s *Signer) MintRoomToken(_ context.Context, subject, room string) (string, time.Time, error) {
	if subject == "" || room == "" {
		return "", time.Time{}, errors.New("massrelay: MintRoomToken: subject and room are required")
	}
	tok, exp, err := s.rs.Mint(subject, []string{room})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("massrelay: minting a relay token: %w", err)
	}
	return tok, exp.UTC(), nil
}

// Authenticator returns the relay authenticator that accepts this signer's
// tokens and requires one on every connection. It also checks the token in a
// page's ReAuth, so a refreshed token passes the same checks.
func (s *Signer) Authenticator() *relaymw.RelayAuthenticator { return s.auth }

// ClientID is the client_id the signer's tokens carry, for an Embedded
// principal on the same relay.
func (s *Signer) ClientID() string { return s.id }
