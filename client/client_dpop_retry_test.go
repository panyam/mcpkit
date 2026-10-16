package client_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProofSource is a lazy, scope-aware core.ProofTokenSource. Its "proof"
// is a counter plus the nonce it was told to use, which is all the transport
// tests need to see.
type fakeProofSource struct {
	mu     sync.Mutex
	armed  bool
	proofs atomic.Int32
	nonce  string
}

func (s *fakeProofSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed {
		return "", core.ErrNoTokenYet
	}
	return "bound-token", nil
}

func (s *fakeProofSource) TokenForScopes([]string) (string, error) {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
	return s.Token()
}

func (s *fakeProofSource) AuthorizeRequest(req *http.Request, token string) error {
	s.mu.Lock()
	nonce := s.nonce
	s.mu.Unlock()
	n := s.proofs.Add(1)
	req.Header.Set("Authorization", "DPoP "+token)
	req.Header.Set("DPoP", "proof-"+string(rune('0'+n))+";nonce="+nonce)
	return nil
}

func (s *fakeProofSource) ObserveResponse(_ *http.Request, resp *http.Response) bool {
	nonce := resp.Header.Get("DPoP-Nonce")
	if nonce == "" {
		return false
	}
	s.mu.Lock()
	s.nonce = nonce
	s.mu.Unlock()
	return resp.StatusCode == http.StatusUnauthorized &&
		strings.Contains(resp.Header.Get("WWW-Authenticate"), "use_dpop_nonce")
}

// nonceServer challenges an unauthenticated request for discovery, then
// demands the nonce "n1" on every DPoP request (RFC 9449 §9). With
// alwaysChallenge it never accepts, to prove the retry does not loop.
func nonceServer(t *testing.T, alwaysChallenge bool) (*httptest.Server, *[]http.Header) {
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `DPoP algs="ES256"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if alwaysChallenge || !strings.HasSuffix(r.Header.Get("DPoP"), ";nonce=n1") {
			w.Header().Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
			w.Header().Set("DPoP-Nonce", "n1")
			http.Error(w, "use_dpop_nonce", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// The first request a lazy DPoP client makes to a nonce-requiring server
// draws two 401s in a row: the discovery challenge that arms the source, then
// use_dpop_nonce on the first authenticated attempt. The nonce retry must not
// count against the single 401 budget the first one spent.
func TestClient_DPoP_NonceChallengeAfterArmingIsAnswered(t *testing.T) {
	srv, seen := nonceServer(t, false)
	src := &fakeProofSource{}
	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, strings.NewReader(`{}`))
	}

	resp, err := client.DoWithAuthRetry(src, buildReq, http.DefaultClient.Do)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, *seen, 3)
	assert.Empty(t, (*seen)[0].Get("Authorization"))
	assert.Equal(t, "DPoP bound-token", (*seen)[1].Get("Authorization"), "token goes under the DPoP scheme, not Bearer")
	assert.Equal(t, "proof-1;nonce=", (*seen)[1].Get("DPoP"))
	assert.Equal(t, "proof-2;nonce=n1", (*seen)[2].Get("DPoP"), "the retry carries a fresh proof with the server's nonce")
}

// A server that keeps challenging gets its 401 back after one nonce retry
// rather than an endless loop.
func TestClient_DPoP_NonceRetryHappensOnce(t *testing.T) {
	srv, seen := nonceServer(t, true)
	src := &fakeProofSource{}
	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, strings.NewReader(`{}`))
	}

	_, err := client.DoWithAuthRetry(src, buildReq, http.DefaultClient.Do)
	var authErr *client.ClientAuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, http.StatusUnauthorized, authErr.StatusCode)
	assert.Len(t, *seen, 3, "discovery 401, the first DPoP attempt, and one nonce retry")
}
