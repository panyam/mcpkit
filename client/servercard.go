package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Errors returned by FetchServerCard and ServerCardCache.Fetch. Wrap-check
// them with errors.Is.
var (
	// ErrCardInsecure means the card URL is not https and
	// WithInsecureCardFetch was not passed. Security-relevant extension
	// info must come from a card fetched over HTTPS.
	ErrCardInsecure = errors.New("mcpkit: server card must be fetched over https")
	// ErrCardCrossOrigin means the card endpoint redirected to another
	// origin. A card reached that way must not be used.
	ErrCardCrossOrigin = errors.New("mcpkit: server card redirected to another origin")
)

// maxCardBytes caps a card body. Cards are small JSON documents.
const (
	maxCardBytes       = 1 << 20
	defaultCardTimeout = 10 * time.Second
)

// CardFetchOption configures FetchServerCard and NewServerCardCache.
type CardFetchOption func(*cardFetchConfig)

type cardFetchConfig struct {
	httpClient *http.Client
	insecure   bool
}

// WithCardHTTPClient sets the HTTP client used to fetch cards. Its
// CheckRedirect is replaced by the same-origin check, and its other settings
// (transport, timeout) are kept. The default is a client with a 10 second
// timeout.
func WithCardHTTPClient(c *http.Client) CardFetchOption {
	return func(cfg *cardFetchConfig) { cfg.httpClient = c }
}

// WithInsecureCardFetch allows http card URLs. Use it for loopback tests and
// local demos only: the Server Card spec requires HTTPS before any
// security-relevant extension info on the card is trusted.
func WithInsecureCardFetch() CardFetchOption {
	return func(cfg *cardFetchConfig) { cfg.insecure = true }
}

// FetchServerCard fetches the SEP-2127 Server Card for the MCP endpoint at
// mcpURL, from <mcpURL>/server-card. It refuses a non-https URL (unless
// WithInsecureCardFetch) and any redirect that leaves mcpURL's origin, so the
// card it returns came from the origin the client connects to. That is the
// condition the Server Card spec sets for trusting security-relevant
// extensionsInfo, such as webhook signing keys.
//
// It decodes the card but does not run ServerCard.Validate, so a card with,
// say, a long description still reaches a caller that only needs one remote.
// Use a ServerCardCache to revalidate with ETags across calls.
func FetchServerCard(ctx context.Context, mcpURL string, opts ...CardFetchOption) (*core.ServerCard, error) {
	return NewServerCardCache(opts...).Fetch(ctx, mcpURL)
}

// ServerCardCache fetches Server Cards and keeps the last copy of each with
// its ETag. Every Fetch revalidates with If-None-Match, so a 304 costs one
// round trip and no body. It is safe for concurrent use.
type ServerCardCache struct {
	client   *http.Client
	insecure bool

	mu      sync.Mutex
	entries map[string]cachedCard
}

type cachedCard struct {
	etag string
	card *core.ServerCard
}

// NewServerCardCache returns an empty cache that fetches with opts.
func NewServerCardCache(opts ...CardFetchOption) *ServerCardCache {
	cfg := cardFetchConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	base := cfg.httpClient
	if base == nil {
		base = &http.Client{Timeout: defaultCardTimeout}
	}
	c := *base
	c.CheckRedirect = sameOriginRedirects
	return &ServerCardCache{client: &c, insecure: cfg.insecure, entries: map[string]cachedCard{}}
}

// Fetch returns the card for mcpURL. On a 304 it returns the cached copy.
// The returned card is shared with the cache, so treat it as read-only.
// Errors wrap ErrCardInsecure, ErrCardCrossOrigin, a non-2xx status or a
// decode failure. A failed fetch keeps the previously cached copy.
func (c *ServerCardCache) Fetch(ctx context.Context, mcpURL string) (*core.ServerCard, error) {
	cardURL, err := serverCardURL(mcpURL)
	if err != nil {
		return nil, err
	}
	if cardURL.Scheme != "https" && !c.insecure {
		return nil, fmt.Errorf("%w: %s", ErrCardInsecure, cardURL)
	}
	key := cardURL.String()

	c.mu.Lock()
	prev, havePrev := c.entries[key]
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", core.ServerCardMediaType+", application/json")
	if havePrev && prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch server card %s: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && havePrev {
		return prev.card, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch server card %s: HTTP %d", key, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch server card %s: %w", key, err)
	}
	if len(body) > maxCardBytes {
		return nil, fmt.Errorf("fetch server card %s: body over %d bytes", key, maxCardBytes)
	}
	var card core.ServerCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("decode server card %s: %w", key, err)
	}
	c.mu.Lock()
	c.entries[key] = cachedCard{etag: resp.Header.Get("ETag"), card: &card}
	c.mu.Unlock()
	return &card, nil
}

// CardRemote returns the remote on card whose url is mcpURL, ignoring a
// trailing slash. A server can list several remotes, and extensionsInfo is
// per remote, so a caller reads the entry for the endpoint it connects to.
func CardRemote(card *core.ServerCard, mcpURL string) (*core.Remote, bool) {
	if card == nil {
		return nil, false
	}
	want := strings.TrimRight(mcpURL, "/")
	for i := range card.Remotes {
		if strings.TrimRight(card.Remotes[i].URL, "/") == want {
			return &card.Remotes[i], true
		}
	}
	return nil, false
}

func serverCardURL(mcpURL string) (*url.URL, error) {
	u, err := url.Parse(mcpURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("server card: %q is not an absolute URL", mcpURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + core.ServerCardPath
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// sameOriginRedirects follows a redirect only within the first request's
// origin: scheme, host and port all equal.
func sameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("server card: stopped after 10 redirects")
	}
	if origin(req.URL) != origin(via[0].URL) {
		return fmt.Errorf("%w: %s to %s", ErrCardCrossOrigin, origin(via[0].URL), origin(req.URL))
	}
	return nil
}

func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + host + ":" + port
}
