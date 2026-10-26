package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

const extensionOrigin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

// originStatus POSTs an initialize to a server built with opts and
// transportOpts, as if it were reached under host, and returns the HTTP
// status. httptest always sends a 127.0.0.1 Host, which is why the remote
// cases need one set explicitly.
func originStatus(t *testing.T, opts []Option, transportOpts []TransportOption, host, origin, bearer string) int {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "origin", Version: "1"}, opts...)
	ts := httptest.NewServer(srv.Handler(append([]TransportOption{WithStreamableHTTP(true)}, transportOpts...)...))
	t.Cleanup(ts.Close)
	body, _ := json.Marshal(&core.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: core.NewRawJSON(json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`))})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A server reached under a public name (#1547). Without WithAllowedOrigins the
// default admits only localhost, by Origin or, when Origin is absent, by Host,
// so every unauthenticated client is refused; listing the server's own host
// admits non-browser and same-origin clients, and an extension needs its own
// entry. A request whose bearer token validates skips the check, because a
// DNS-rebinding page cannot attach one.
func TestOrigin_RemoteServer(t *testing.T) {
	const host = "mcp.example.com"
	bearer := []Option{WithBearerToken("s3cret")}
	ownHost := []TransportOption{WithAllowedOrigins(host)}
	withExtension := []TransportOption{WithAllowedOrigins(host, extensionOrigin)}
	for _, tc := range []struct {
		name          string
		opts          []Option
		transportOpts []TransportOption
		host, origin  string
		token         string
		forbidden     bool
	}{
		{"default refuses a client with no Origin", nil, nil, host, "", "", true},
		{"default refuses an extension", nil, nil, host, extensionOrigin, "", true},
		{"own host admits a client with no Origin", nil, ownHost, host, "", "", false},
		{"own host admits same-origin", nil, ownHost, host, "https://" + host, "", false},
		{"own host still refuses an extension", nil, ownHost, host, extensionOrigin, "", true},
		{"listed extension is admitted", nil, withExtension, host, extensionOrigin, "", false},
		{"valid bearer admits an extension by default", bearer, nil, host, extensionOrigin, "s3cret", false},
		{"valid bearer admits a client with no Origin by default", bearer, nil, host, "", "s3cret", false},
		{"invalid bearer is still refused", bearer, nil, host, extensionOrigin, "wrong", true},
		{"no bearer on an auth server is still refused", bearer, nil, host, extensionOrigin, "", true},
		// The DNS-rebinding shape against a local server: the attacker's page
		// is same-origin with its own name, and has no token to send.
		{"rebinding without a token is refused", bearer, nil, "evil.example", "http://evil.example", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := originStatus(t, tc.opts, tc.transportOpts, tc.host, tc.origin, tc.token)
			if (got == http.StatusForbidden) != tc.forbidden {
				t.Errorf("HTTP %d, want forbidden=%v", got, tc.forbidden)
			}
		})
	}
}
