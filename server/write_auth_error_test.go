package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
)

// headeredAuthError stands in for a validator in a sub-module that wraps
// *core.AuthError to carry extra headers, as ext/auth does for DPoP-Nonce.
type headeredAuthError struct {
	*core.AuthError
	h http.Header
}

func (e *headeredAuthError) Unwrap() error                { return e.AuthError }
func (e *headeredAuthError) ResponseHeaders() http.Header { return e.h }

func TestWriteAuthError_CopiesResponseHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("DPoP-Nonce", "n1")
	err := &headeredAuthError{
		AuthError: &core.AuthError{Code: http.StatusUnauthorized, Message: "use_dpop_nonce", WWWAuthenticate: `DPoP error="use_dpop_nonce"`},
		h:         h,
	}

	rec := httptest.NewRecorder()
	writeAuthError(rec, err)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `DPoP error="use_dpop_nonce"`, rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, "n1", rec.Header().Get("DPoP-Nonce"))
}

func TestWriteAuthError_PlainAuthErrorUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthError(rec, &core.AuthError{Code: http.StatusForbidden, Message: "no", WWWAuthenticate: "Bearer x"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "Bearer x", rec.Header().Get("WWW-Authenticate"))
	assert.Empty(t, rec.Header().Get("DPoP-Nonce"))
}
