package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// metronomeConsentPage is metronome-mcp.fly.dev's /authorize page as served
// on 2026-10-14, trimmed to the form. The subject field is filled by script
// in a browser; served, it is empty.
const metronomeConsentPage = `<!DOCTYPE html><html><body><h1>Demo authorization</h1>
<form method="post" action="/consent" onsubmit="try{localStorage.setItem('x',1)}catch(e){}">
<input type="hidden" name="state" value="st&amp;1">
<label>Your name: <input type="text" name="subject" id="subject" value=""></label>
<button type="submit" name="action" value="approve">Approve</button>
<button type="submit" name="action" value="deny">Deny</button>
</form></body></html>`

// consentStack serves page at /authorize, records the form posted to
// /consent, and redirects that post to a loopback callback, which records
// that it was reached.
func consentStack(t *testing.T, path, page string, status int) (authURL string, posted *url.Values, reached *bool) {
	t.Helper()
	posted, reached = &url.Values{}, new(bool)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { *reached = true }))
	t.Cleanup(callback.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("/consent", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*posted = r.PostForm
		http.Redirect(w, r, callback.URL+"/callback?code=c&state=s", http.StatusFound)
	})
	mux.HandleFunc("/redirect-now", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, callback.URL+"/callback?code=c&state=s", http.StatusFound)
	})
	as := httptest.NewServer(mux)
	t.Cleanup(as.Close)
	q := url.Values{"redirect_uri": {callback.URL + "/callback"}, "state": {"s"}}
	return as.URL + path + "?" + q.Encode(), posted, reached
}

func TestApproveHeadless_SubmitsMetronomeConsentForm(t *testing.T) {
	authURL, posted, reached := consentStack(t, "/authorize", metronomeConsentPage, http.StatusOK)
	if err := approveHeadless(authURL); err != nil {
		t.Fatalf("approveHeadless = %v", err)
	}
	if got := posted.Get("action"); got != "approve" {
		t.Errorf("action = %q, want approve (never deny)", got)
	}
	if got := posted.Get("state"); got != "st&1" {
		t.Errorf("state = %q, want the hidden field unescaped", got)
	}
	if _, ok := (*posted)["subject"]; !ok {
		t.Error("the text input was not submitted")
	}
	if !*reached {
		t.Error("the loopback callback was never reached")
	}
}

func TestApproveHeadless_ImmediateRedirectNeedsNoForm(t *testing.T) {
	authURL, posted, reached := consentStack(t, "/redirect-now", "", http.StatusOK)
	if err := approveHeadless(authURL); err != nil {
		t.Fatalf("approveHeadless = %v", err)
	}
	if len(*posted) != 0 {
		t.Errorf("posted %v to a server that never asked", *posted)
	}
	if !*reached {
		t.Error("the loopback callback was never reached")
	}
}

func TestApproveHeadless_RefusesFormWithoutApprove(t *testing.T) {
	page := `<form method="post" action="/consent"><input type="password" name="pw" value=""><button type="submit" name="go" value="login">Log in</button></form>`
	authURL, posted, reached := consentStack(t, "/authorize", page, http.StatusOK)
	err := approveHeadless(authURL)
	if err == nil || !strings.Contains(err.Error(), "no approve button") {
		t.Fatalf("approveHeadless = %v, want a refusal naming the missing approve button", err)
	}
	if len(*posted) != 0 || *reached {
		t.Error("a login page must not be submitted")
	}
}

// The redirect_uri mismatch metronome reports is a 400 page with no form. It
// must surface as an error at once, not leave the login waiting for a
// callback that never comes.
func TestApproveHeadless_ErrorPageFailsFast(t *testing.T) {
	authURL, _, _ := consentStack(t, "/authorize", `{"error":"invalid_request","error_description":"redirect_uri is not registered"}`, http.StatusBadRequest)
	err := approveHeadless(authURL)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("approveHeadless = %v, want an error carrying the 400", err)
	}
}
