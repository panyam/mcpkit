package events

// Path (d) of spec §"Webhook Security" → "Endpoint verification": the
// callback URL's https origin serves a document listing the path prefixes
// that accept MCP webhook deliveries, and a URL it covers is verified without
// a challenge POST, since control of the origin stands in for proof of intent.
// Opt-in via WithWellKnownReceiverDocs; issue 1436.
//
// A document that is missing, malformed, oversized, redirected or served over
// http never fails a subscribe. It only fails to verify, and the handshake
// decides instead.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WellKnownReceiverPath is where a receiver publishes the document, relative
// to its callback URLs' origin.
const WellKnownReceiverPath = "/.well-known/mcp-webhook-receiver.json"

const (
	// maxWellKnownDocBytes caps the document read. It lists path prefixes;
	// anything larger is not a receiver document.
	maxWellKnownDocBytes = 64 << 10
	// Freshness bounds for a fetched document. Cache-Control max-age is
	// honoured inside them; without one the default applies.
	defaultWellKnownTTL = 5 * time.Minute
	maxWellKnownTTL     = time.Hour
	// missingWellKnownTTL is how long an origin without a usable document is
	// left alone, so a host that publishes none is not asked on every
	// subscribe.
	missingWellKnownTTL = time.Minute
)

// WithWellKnownReceiverDocs lets a receiver-published
// /.well-known/mcp-webhook-receiver.json verify callback URLs under its
// origin (spec path (d)). It is checked after the allowlist and
// pre-verifiers and before the challenge.
//
// Only https origins count, because the document stands in for proof that
// the receiver controls the origin. The fetch goes through the same
// SSRF-guarded, redirect-refusing client as deliveries, is capped at 64 KiB,
// and spends one token of the destination host's verification budget
// (WithVerificationRateLimit) on a cache miss. Documents are cached per
// origin, honouring Cache-Control max-age up to an hour (5 minutes when
// absent); an origin without one is cached as such for a minute. A match is
// recorded per (principal, url) like the other paths.
//
// Off by default: it sends a request to every new callback origin, and the
// path is the part of the verification section most likely to change during
// SEP review.
func WithWellKnownReceiverDocs() WebhookOption {
	return func(r *WebhookRegistry) {
		r.wellKnown = &wellKnownCache{entries: map[string]wellKnownEntry{}}
	}
}

// WellKnownReceiverHandler serves the receiver document declaring that
// callback URLs under each of prefixes accept MCP webhook deliveries. Mount
// it at WellKnownReceiverPath on the origin the callbacks use, over https.
//
// A prefix matches at a path-segment boundary: "/hooks" covers "/hooks" and
// "/hooks/a" but not "/hooksx". Publishing the document also tells anyone who
// fetches it where your webhook endpoints are; receivers that would rather
// not can rely on the challenge handshake instead.
func WellKnownReceiverHandler(prefixes ...string) http.Handler {
	body, _ := json.Marshal(struct {
		Receivers []string `json:"receivers"`
	}{Receivers: append([]string{}, prefixes...)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write(body)
	})
}

type wellKnownEntry struct {
	prefixes []string // nil when the origin has no usable document
	expires  time.Time
}

type wellKnownCache struct {
	mu      sync.Mutex
	entries map[string]wellKnownEntry // origin → document
}

// wellKnownCovers reports whether deliveryURL is covered by its origin's
// receiver document, fetching and caching the document as needed.
func (r *WebhookRegistry) wellKnownCovers(ctx context.Context, deliveryURL string, now time.Time) bool {
	u, err := url.Parse(deliveryURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return false
	}
	origin := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)

	r.wellKnown.mu.Lock()
	entry, ok := r.wellKnown.entries[origin]
	r.wellKnown.mu.Unlock()
	if !ok || now.After(entry.expires) {
		if !r.verifyLimit.allow(strings.ToLower(u.Hostname()), now) {
			return false
		}
		entry = r.fetchWellKnown(ctx, origin, now)
		r.wellKnown.mu.Lock()
		r.wellKnown.entries[origin] = entry
		r.wellKnown.mu.Unlock()
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, p := range entry.prefixes {
		if pathHasPrefixSegment(path, p) {
			return true
		}
	}
	return false
}

func (r *WebhookRegistry) fetchWellKnown(ctx context.Context, origin string, now time.Time) wellKnownEntry {
	missing := wellKnownEntry{expires: now.Add(missingWellKnownTTL)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+WellKnownReceiverPath, nil)
	if err != nil {
		return missing
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.logf("[webhook] well-known receiver document fetch from %s failed: %v", origin, err)
		return missing
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return missing
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWellKnownDocBytes+1))
	if err != nil || len(body) > maxWellKnownDocBytes {
		return missing
	}
	var doc struct {
		Receivers []string `json:"receivers"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Receivers) == 0 {
		return missing
	}
	var prefixes []string
	for _, p := range doc.Receivers {
		if strings.HasPrefix(p, "/") {
			prefixes = append(prefixes, p)
		}
	}
	if len(prefixes) == 0 {
		return missing
	}
	return wellKnownEntry{prefixes: prefixes, expires: now.Add(wellKnownTTL(resp.Header.Get("Cache-Control")))}
}

// wellKnownTTL reads max-age from a Cache-Control header, bounded by
// maxWellKnownTTL. no-store and no-cache mean "do not reuse", which is a
// zero TTL: the document still counts for this subscribe.
func wellKnownTTL(cacheControl string) time.Duration {
	ttl := defaultWellKnownTTL
	for _, directive := range strings.Split(cacheControl, ",") {
		d := strings.ToLower(strings.TrimSpace(directive))
		switch {
		case d == "no-store" || d == "no-cache":
			return 0
		case strings.HasPrefix(d, "max-age="):
			if secs, err := strconv.Atoi(strings.TrimPrefix(d, "max-age=")); err == nil && secs >= 0 {
				ttl = time.Duration(secs) * time.Second
			}
		}
	}
	if ttl > maxWellKnownTTL {
		ttl = maxWellKnownTTL
	}
	return ttl
}

// pathHasPrefixSegment matches prefix against path at a segment boundary, the
// same rule the allowlist uses.
func pathHasPrefixSegment(path, prefix string) bool {
	trimmed := strings.TrimSuffix(prefix, "/")
	return trimmed == "" || path == trimmed || strings.HasPrefix(path, trimmed+"/")
}
