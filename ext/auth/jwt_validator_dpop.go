package auth

import (
	"errors"
	"net/http"
	"strings"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/apiauth"
)

// credentials returns the scheme and token from the Authorization header.
// The scheme is "Bearer", or "DPoP" when the validator has DPoP wired. Both
// are matched case-insensitively (RFC 9110 §11.1). An empty token means no
// usable credentials, which the caller answers with a plain 401.
func (v *JWTValidator) credentials(r *http.Request) (scheme, token string) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok {
		return "", ""
	}
	token = strings.TrimSpace(token)
	switch {
	case strings.EqualFold(scheme, apiauth.TokenTypeBearer):
		return apiauth.TokenTypeBearer, token
	case strings.EqualFold(scheme, apiauth.TokenTypeDPoP) && v.DPoP != nil:
		return apiauth.TokenTypeDPoP, token
	}
	return "", ""
}

// cnfJKT reads cnf.jkt, the RFC 7638 thumbprint of the key a token is bound
// to (RFC 9449 §6.1). Empty means the token is not DPoP-bound.
func cnfJKT(claims map[string]any) string {
	cnf, _ := claims["cnf"].(map[string]any)
	jkt, _ := cnf["jkt"].(string)
	return jkt
}

// enforceBinding applies the RFC 9449 §7 rules that decide whether this
// presentation of an already-valid token is legitimate. It mirrors oneauth's
// APIMiddleware.enforceBinding, which mcpkit cannot call directly because the
// JWT path here does its own kid-based JWKS validation.
//
//   - Under the DPoP scheme there must be exactly one proof, signed by the
//     key cnf.jkt names, for this method and URL and this token (`ath`).
//   - Under the Bearer scheme a bound token is refused, because otherwise a
//     thief drops the proof and the binding buys nothing (§7.2).
//
// Nothing changes for a validator without DPoP wired.
func (v *JWTValidator) enforceBinding(r *http.Request, scheme, token, jkt string) error {
	if v.DPoP == nil {
		return nil
	}

	if scheme == apiauth.TokenTypeBearer {
		if v.RequireDPoP {
			return v.dpopError("invalid_token", "this resource requires a DPoP-bound access token", "")
		}
		if jkt != "" {
			return v.dpopError("invalid_token", "DPoP-bound token presented as a bearer token", "")
		}
		return nil
	}

	proven, err := v.DPoP.ConfirmResource(r.Context(), r, token)
	if err != nil {
		var nonceErr *apiauth.NonceRequiredError
		if errors.As(err, &nonceErr) {
			return v.dpopError(apiauth.ErrorUseDPoPNonce, nonceErr.Error(), nonceErr.Nonce)
		}
		description := "invalid DPoP proof"
		var ge *apiauth.GrantError
		if errors.As(err, &ge) && ge.Description != "" {
			description = ge.Description
		}
		return v.dpopError("invalid_dpop_proof", description, "")
	}

	if jkt == "" {
		return v.dpopError("invalid_token", "token is not bound to a DPoP key", "")
	}
	if proven == nil || proven.JKT != jkt {
		return v.dpopError("invalid_token", "DPoP proof key does not match the token binding", "")
	}
	return nil
}

// dpopError is a 401 carrying a DPoP challenge with an error code, plus the
// DPoP-Nonce to use next when the code is use_dpop_nonce.
func (v *JWTValidator) dpopError(code, description, nonce string) error {
	ae := &mcpcore.AuthError{
		Code:            http.StatusUnauthorized,
		Message:         description,
		WWWAuthenticate: v.dpopChallenge(code, description),
	}
	if nonce == "" {
		return ae
	}
	h := http.Header{}
	h.Set(apiauth.DPoPNonceHeader, nonce)
	return &authErrorWithHeaders{AuthError: ae, headers: h}
}

// challenge401 is the WWW-Authenticate value for a request with no usable
// credentials. A bearer-only validator keeps its existing Bearer challenge.
// With DPoP wired, the DPoP challenge (with the accepted algs) follows it,
// so a client can tell the server takes both. With RequireDPoP, only the
// DPoP challenge is sent.
func (v *JWTValidator) challenge401() string {
	bearer := WWWAuth401(v.ResourceMetadataURL, v.AllScopes...)
	if v.DPoP == nil {
		return bearer
	}
	dpop := v.dpopChallenge("", "")
	if v.RequireDPoP {
		return dpop
	}
	return bearer + ", " + dpop
}

// dpopChallenge builds a `DPoP` challenge (RFC 9449 §7.1): the error and its
// description when there is one, then algs, resource_metadata and scope.
func (v *JWTValidator) dpopChallenge(code, description string) string {
	var parts []string
	if code != "" {
		parts = append(parts, `error="`+quoteParam(code)+`"`)
		if description != "" {
			parts = append(parts, `error_description="`+quoteParam(description)+`"`)
		}
	}
	if algs := v.DPoP.SigningAlgValuesSupported(); len(algs) > 0 {
		parts = append(parts, `algs="`+strings.Join(algs, " ")+`"`)
	}
	if v.ResourceMetadataURL != "" {
		parts = append(parts, `resource_metadata="`+quoteParam(v.ResourceMetadataURL)+`"`)
	}
	if len(v.AllScopes) > 0 {
		parts = append(parts, `scope="`+quoteParam(strings.Join(v.AllScopes, " "))+`"`)
	}
	return "DPoP " + strings.Join(parts, ", ")
}

// inScheme rewrites a Bearer challenge onto the DPoP scheme when that is the
// scheme the request used. RFC 9449 §7.1 has errors reported against the
// scheme the client presented, so a DPoP client refused for scope gets a
// DPoP challenge it recognises.
func inScheme(scheme, challenge string) string {
	if scheme == apiauth.TokenTypeDPoP {
		return "DPoP " + strings.TrimPrefix(challenge, "Bearer ")
	}
	return challenge
}

// quoteParam escapes a value for an RFC 7235 quoted-string.
func quoteParam(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// authErrorWithHeaders is a core.AuthError that also needs response headers,
// here the DPoP-Nonce of a §9 nonce challenge. The server's auth error writer
// finds both through errors.As: the AuthError via Unwrap, and the headers via
// core.AuthResponseHeaders, which this satisfies by its method set alone.
type authErrorWithHeaders struct {
	*mcpcore.AuthError
	headers http.Header
}

func (e *authErrorWithHeaders) Unwrap() error { return e.AuthError }

// ResponseHeaders implements core.AuthResponseHeaders.
func (e *authErrorWithHeaders) ResponseHeaders() http.Header { return e.headers }
