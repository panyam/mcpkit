package events

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// UnsafeAllowCallbackOrigins permits delivery URLs under each of origins past
// both callback guards, the https requirement and the SSRF refusal of
// loopback, private and other non-routable addresses, for the rest of the
// registry's life. Every other URL stays subject to both. It returns the
// origins in normalized form (lowercase, default port omitted), in the order
// given.
//
// Each origin must be an origin and nothing more: http or https, a host, an
// optional port, and no userinfo, path, query or fragment. The call is
// all-or-nothing: if any entry is malformed it returns an error and permits
// none of them, so a typo cannot leave a list half applied. Permitting an
// origin twice is a no-op, calling with no origins does nothing, and there is
// no way to withdraw one.
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
// This exists so a conformance harness, or a test standing up several local
// receivers, can take deliveries on loopback from a server whose guards are
// otherwise at their defaults. A harness chooses its ports at run time, which
// is why this is a method rather than a WebhookOption. Unsafe-prefixed because
// it switches off SSRF protection for destinations the caller names; no
// production deployment should call it with an address it does not control.
func (r *WebhookRegistry) UnsafeAllowCallbackOrigins(origins ...string) ([]string, error) {
	type parsed struct{ key, addr, normalized string }
	ps := make([]parsed, 0, len(origins))
	for _, origin := range origins {
		u, err := parseOrigin(origin)
		if err != nil {
			return nil, err
		}
		key, addr := originKey(u)
		normalized := u.Scheme + "://" + addr
		if port := u.Port(); port == "" || port == defaultPort(u.Scheme) {
			normalized = u.Scheme + "://" + bracketHost(strings.ToLower(u.Hostname()))
		}
		ps = append(ps, parsed{key, addr, normalized})
	}
	if len(ps) == 0 {
		return nil, nil
	}

	r.callbackOriginsMu.Lock()
	if r.callbackOrigins == nil {
		r.callbackOrigins = map[string]struct{}{}
		r.callbackDialAddrs = map[string]struct{}{}
	}
	for _, p := range ps {
		r.callbackOrigins[p.key] = struct{}{}
		r.callbackDialAddrs[p.addr] = struct{}{}
	}
	r.callbackOriginsMu.Unlock()

	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.normalized
		r.logf("[webhook] UnsafeAllowCallbackOrigins: %s is now exempt from the https and SSRF guards", p.normalized)
	}
	return out, nil
}

func parseOrigin(origin string) (*url.URL, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, fmt.Errorf("origin %q: %w", origin, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("origin %q: scheme must be http or https", origin)
	case u.Hostname() == "":
		return nil, fmt.Errorf("origin %q: host is required", origin)
	case u.User != nil:
		return nil, fmt.Errorf("origin %q: userinfo is not part of an origin", origin)
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, fmt.Errorf("origin %q: path, query and fragment are not part of an origin", origin)
	}
	return u, nil
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
