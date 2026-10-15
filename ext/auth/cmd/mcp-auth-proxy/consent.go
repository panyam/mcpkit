package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

var (
	formRe   = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	actionRe = regexp.MustCompile(`(?i)\baction\s*=\s*"([^"]*)"`)
	fieldRe  = regexp.MustCompile(`(?is)<(input|button)\b([^>]*)>`)
	attrRe   = regexp.MustCompile(`(?i)\b(name|value|type)\s*=\s*"([^"]*)"`)
	approves = []string{"approve", "allow", "accept", "authorize", "consent", "yes"}
)

// approveHeadless walks an authorization request the way a user clicking
// Approve would. Redirects are followed (with cookies kept, since consent
// forms usually key on a session), so a server that redirects straight to the
// request's redirect_uri completes on the GET. Otherwise the first form on the page
// is submitted with its hidden and text inputs as served, plus the one submit
// button whose value reads as approval; a form with no such button is an
// error rather than a guess.
func approveHeadless(authURL string) error {
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	redirect := u.Query().Get("redirect_uri")
	if redirect == "" {
		return errors.New("authorization URL carries no redirect_uri")
	}
	arrived := func(r *http.Response) bool { return strings.HasPrefix(r.Request.URL.String(), redirect) }
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	resp, err := hc.Get(authURL)
	if err != nil {
		return err
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if arrived(resp) {
		return nil
	}
	form := formRe.FindSubmatch(page)
	if form == nil {
		return fmt.Errorf("authorization page (%s) has no form to approve and did not redirect", resp.Status)
	}
	action := resp.Request.URL
	if m := actionRe.FindSubmatch(form[1]); m != nil {
		if action, err = resp.Request.URL.Parse(html.UnescapeString(string(m[1]))); err != nil {
			return err
		}
	}
	values := url.Values{}
	approved := false
	for _, f := range fieldRe.FindAllSubmatch(form[2], -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllSubmatch(f[2], -1) {
			attrs[strings.ToLower(string(a[1]))] = html.UnescapeString(string(a[2]))
		}
		name := attrs["name"]
		if name == "" {
			continue
		}
		isSubmit := strings.EqualFold(string(f[1]), "button") || attrs["type"] == "submit"
		if !isSubmit {
			values.Set(name, attrs["value"])
			continue
		}
		for _, a := range approves {
			if !approved && strings.EqualFold(attrs["value"], a) {
				values.Set(name, attrs["value"])
				approved = true
			}
		}
	}
	if !approved {
		return errors.New("authorization form has no approve button; run with -headless=false")
	}
	resp, err = hc.PostForm(action.String(), values)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if !arrived(resp) {
		return fmt.Errorf("approving the consent form ended at %s (%s), not the redirect_uri", resp.Request.URL.Redacted(), resp.Status)
	}
	return nil
}
