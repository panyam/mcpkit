package events

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// UnsafeAllowCallbackOrigin permits delivery URLs under one origin past both
// callback guards, the https requirement and the SSRF refusal of loopback,
// private and other non-routable addresses, for the rest of the registry's
// life. Every other URL stays subject to both. It returns the origin in its
// normalized form (lowercase, default port omitted).
//
// origin must be an origin and nothing more: http or https, a host, an
// optional port, and no userinfo, path, query or fragment. Anything else is
// an error and permits nothing. Permitting the same origin twice is a no-op,
// and there is no way to withdraw one.
//
// Endpoint verification and the redirect refusal are unaffected, so a
// permitted receiver still has to answer the challenge and still cannot
// bounce a delivery elsewhere.
//
// The dial-time exemption matches the host and port the transport dials,
// before DNS resolution. A hostname origin is therefore trusted for whatever
// it resolves to, including after a rebinding, which is what permitting an
// origin means and why delivery-time revalidation says nothing about one.
//
// This exists so a conformance harness can receive deliveries on loopback
// from a server whose guards are otherwise at their defaults, after grading
// that the guards refused it. The harness chooses its port at run time,
// which is why this is a method rather than a WebhookOption. Unsafe-prefixed
// because it switches off SSRF protection for a destination the caller
// names; no production deployment should call it with an address it does
// not control.
func (r *WebhookRegistry) UnsafeAllowCallbackOrigin(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("origin %q: %w", origin, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("origin %q: scheme must be http or https", origin)
	case u.Hostname() == "":
		return "", fmt.Errorf("origin %q: host is required", origin)
	case u.User != nil:
		return "", fmt.Errorf("origin %q: userinfo is not part of an origin", origin)
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", fmt.Errorf("origin %q: path, query and fragment are not part of an origin", origin)
	}
	key, addr := originKey(u)

	r.callbackOriginsMu.Lock()
	if r.callbackOrigins == nil {
		r.callbackOrigins = map[string]struct{}{}
		r.callbackDialAddrs = map[string]struct{}{}
	}
	r.callbackOrigins[key] = struct{}{}
	r.callbackDialAddrs[addr] = struct{}{}
	r.callbackOriginsMu.Unlock()

	normalized := u.Scheme + "://" + addr
	if port := u.Port(); port == "" || port == defaultPort(u.Scheme) {
		normalized = u.Scheme + "://" + bracketHost(strings.ToLower(u.Hostname()))
	}
	r.logf("[webhook] UnsafeAllowCallbackOrigin: %s is now exempt from the https and SSRF guards", normalized)
	return normalized, nil
}

// originKey returns u's origin as "scheme://host:port" and the "host:port"
// a transport would dial for it, both with the port made explicit so the
// default-port and explicit-port spellings of one origin compare equal.
func originKey(u *url.URL) (key, dialAddr string) {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	dialAddr = net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	return scheme + "://" + dialAddr, dialAddr
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func bracketHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func (r *WebhookRegistry) callbackOriginPermitted(u *url.URL) bool {
	if u.Hostname() == "" {
		return false
	}
	key, _ := originKey(u)
	r.callbackOriginsMu.RLock()
	defer r.callbackOriginsMu.RUnlock()
	_, ok := r.callbackOrigins[key]
	return ok
}

func (r *WebhookRegistry) callbackDialAddrPermitted(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	r.callbackOriginsMu.RLock()
	defer r.callbackOriginsMu.RUnlock()
	_, ok := r.callbackDialAddrs[net.JoinHostPort(strings.ToLower(host), port)]
	return ok
}
