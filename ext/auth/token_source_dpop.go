package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// AuthorizeRequest presents token on req. Without a DPoPKey it is the Bearer
// scheme. With one, the token goes under the DPoP scheme with a fresh proof
// bound to it (`ath`), carrying the last nonce this request's origin issued.
//
// The client transport calls it in place of setting the header itself, by
// way of the core.ProofTokenSource interface.
func (s *OAuthTokenSource) AuthorizeRequest(req *http.Request, token string) error {
	if s.DPoPKey == nil {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
	proof, err := s.DPoPKey.ProofWithNonce(req.Method, req.URL.String(), token, s.dpopNonce(req.URL))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "DPoP "+token)
	req.Header.Set("DPoP", proof)
	return nil
}

// ObserveResponse keeps any DPoP-Nonce the MCP server sends, on any response
// (RFC 9449 §8.2), and reports whether resp is a use_dpop_nonce challenge the
// request should be retried after (§9).
func (s *OAuthTokenSource) ObserveResponse(req *http.Request, resp *http.Response) bool {
	if s.DPoPKey == nil || resp == nil {
		return false
	}
	nonce := resp.Header.Get("DPoP-Nonce")
	if nonce == "" {
		return false
	}
	s.setDPoPNonce(req.URL, nonce)
	return resp.StatusCode == http.StatusUnauthorized &&
		strings.Contains(resp.Header.Get("WWW-Authenticate"), "use_dpop_nonce")
}

func (s *OAuthTokenSource) dpopNonce(u *url.URL) string {
	s.dpopMu.Lock()
	defer s.dpopMu.Unlock()
	return s.dpopNonces[nonceOrigin(u)]
}

func (s *OAuthTokenSource) setDPoPNonce(u *url.URL, nonce string) {
	s.dpopMu.Lock()
	defer s.dpopMu.Unlock()
	if s.dpopNonces == nil {
		s.dpopNonces = map[string]string{}
	}
	s.dpopNonces[nonceOrigin(u)] = nonce
}

// nonceOrigin is the granularity a nonce belongs to: one server, not one
// path.
func nonceOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}
