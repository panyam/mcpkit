package main

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mintIDToken builds the demo IdP id_token: a plain RS256 JWT the IdP
// issues for the user. This is the `subject_token` the MCP client presents
// to the IdP token-exchange endpoint (stage 1 of the EMA chain). It stays
// as the demo login stand-in — a real deployment gets this from the
// enterprise IdP's normal OIDC login, not a demo minter. The IdP validates
// it against its own trusted-issuer entry before minting the ID-JAG, so its
// `aud` must match the IdP's expected assertion audience (the IdP issuer URL
// here).
func mintIDToken(idpPriv *rsa.PrivateKey, idpIssuer, sub, aud string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": idpIssuer,
		"sub": sub,
		"aud": aud,
		"iat": now.Unix(),
		"exp": now.Add(1 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(idpPriv)
	if err != nil {
		return "", fmt.Errorf("sign id_token: %w", err)
	}
	return signed, nil
}
