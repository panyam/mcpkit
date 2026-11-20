package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// serverCardConfig holds what WithServerCard adds on top of the server's
// own Implementation.
type serverCardConfig struct {
	name        string
	description string
	repository  *core.Repository
	remotes     []core.Remote
	meta        map[string]any
	maxAge      time.Duration
}

// ServerCardOption configures WithServerCard.
type ServerCardOption func(*serverCardConfig)

// defaultCardMaxAge is the Cache-Control max-age when WithCardMaxAge is not
// set. It is short because extensionsInfo can carry rotating keys, and the
// ETag makes a refetch cheap.
const defaultCardMaxAge = 5 * time.Minute

// WithServerCard serves a SEP-2127 Server Card at <prefix>/server-card on the
// streamable HTTP transport. The card takes name, version, title,
// description, websiteUrl and icons from the server's Implementation, and its
// remote's supportedProtocolVersions from the versions the endpoint accepts,
// so it cannot drift from what server/discover reports.
//
// The route sits outside the Origin guard and bearer auth, because a card is
// public metadata a host reads before it connects. A WithHandlerWrap
// middleware still wraps it.
//
// The card must pass core.ServerCard.Validate. Implementation.Name is often
// not reverse-DNS, so most servers also pass WithCardName. Handler panics
// on an invalid card, the same way a bad WithMethodHandler does, so the
// mistake shows at startup and not on a host's first fetch.
//
//	srv := server.NewServer(core.Implementation{Name: "weather", Version: "1.2.0"},
//	    server.WithServerCard(
//	        server.WithCardName("io.github.acme/weather"),
//	        server.WithCardDescription("Forecasts and alerts for any city"),
//	    ),
//	)
func WithServerCard(opts ...ServerCardOption) Option {
	return func(o *serverOptions) {
		cfg := &serverCardConfig{maxAge: defaultCardMaxAge}
		for _, opt := range opts {
			opt(cfg)
		}
		o.serverCard = cfg
	}
}

// WithCardName sets the card's reverse-DNS namespace/name, such as
// "io.github.acme/weather". Without it the card uses Implementation.Name.
func WithCardName(name string) ServerCardOption {
	return func(c *serverCardConfig) { c.name = name }
}

// WithCardDescription sets the card's description, 1 to 100 characters.
// Without it the card uses Implementation.Description.
func WithCardDescription(description string) ServerCardOption {
	return func(c *serverCardConfig) { c.description = description }
}

// WithCardRepository sets the card's source repository.
func WithCardRepository(repo core.Repository) ServerCardOption {
	return func(c *serverCardConfig) { c.repository = &repo }
}

// WithCardRemotes replaces the remote the server derives from each request.
// Use it when the public URL differs from what the request shows, such as
// behind a proxy that rewrites the path. A remote with no
// SupportedProtocolVersions gets the endpoint's versions filled in, and every
// streamable-http remote gets the SetCardExtensionInfo entries.
func WithCardRemotes(remotes ...core.Remote) ServerCardOption {
	return func(c *serverCardConfig) { c.remotes = remotes }
}

// WithCardMeta sets the card's _meta object.
func WithCardMeta(meta map[string]any) ServerCardOption {
	return func(c *serverCardConfig) { c.meta = meta }
}

// WithCardMaxAge sets the Cache-Control max-age on the card. The default is
// five minutes. Zero sends no-cache, so hosts revalidate with the ETag on
// every fetch.
func WithCardMaxAge(d time.Duration) ServerCardOption {
	return func(c *serverCardConfig) { c.maxAge = d }
}

// cardExtensionInfo holds the SetCardExtensionInfo providers by extension id.
type cardExtensionInfo struct {
	mu        sync.RWMutex
	providers map[string]func(remoteURL string) map[string]any
}

// SetCardExtensionInfo registers fn to fill remotes[].extensionsInfo[id] on
// the Server Card. fn runs on every card request, once per streamable-http
// remote, with that remote's URL. A nil result leaves the entry out. A second
// call with the same id replaces the first, and a nil fn removes it.
//
// An entry carries server-level data the extension defines, such as the
// SEP-3415 webhook signing keys. It does not declare support for the
// extension, which is still negotiated through capabilities.extensions.
// Extensions call this on the server they are registered with, so no
// extension imports another to reach the card.
func (s *Server) SetCardExtensionInfo(id string, fn func(remoteURL string) map[string]any) {
	s.cardExtInfo.mu.Lock()
	defer s.cardExtInfo.mu.Unlock()
	if fn == nil {
		delete(s.cardExtInfo.providers, id)
		return
	}
	if s.cardExtInfo.providers == nil {
		s.cardExtInfo.providers = map[string]func(string) map[string]any{}
	}
	s.cardExtInfo.providers[id] = fn
}

// cardExtensionsFor runs every provider for one remote URL. Map iteration
// order does not reach the wire, because encoding/json sorts map keys.
func (s *Server) cardExtensionsFor(remoteURL string) map[string]map[string]any {
	s.cardExtInfo.mu.RLock()
	defer s.cardExtInfo.mu.RUnlock()
	var out map[string]map[string]any
	for id, fn := range s.cardExtInfo.providers {
		if v := fn(remoteURL); v != nil {
			if out == nil {
				out = map[string]map[string]any{}
			}
			out[id] = v
		}
	}
	return out
}

// buildServerCard assembles the card for one request. remoteURL is the
// derived streamable URL, used when WithCardRemotes is not set.
func (s *Server) buildServerCard(remoteURL string, versions []string) core.ServerCard {
	cfg := s.options.serverCard
	info := s.dispatcher.serverInfo
	card := core.ServerCard{
		Schema:      core.ServerCardSchemaURL,
		Name:        cfg.name,
		Version:     info.Version,
		Description: cfg.description,
		Title:       info.Title,
		WebsiteURL:  info.WebsiteURL,
		Repository:  cfg.repository,
		Icons:       info.Icons,
		Meta:        cfg.meta,
	}
	if card.Name == "" {
		card.Name = info.Name
	}
	if card.Description == "" {
		card.Description = info.Description
	}
	remotes := cfg.remotes
	if len(remotes) == 0 {
		remotes = []core.Remote{{Type: core.RemoteTypeStreamableHTTP, URL: remoteURL}}
	}
	card.Remotes = make([]core.Remote, len(remotes))
	for i, r := range remotes {
		if len(r.SupportedProtocolVersions) == 0 {
			r.SupportedProtocolVersions = versions
		}
		if r.Type == core.RemoteTypeStreamableHTTP {
			if ext := s.cardExtensionsFor(r.URL); ext != nil {
				merged := make(map[string]map[string]any, len(ext))
				for k, v := range r.ExtensionsInfo {
					merged[k] = v
				}
				for k, v := range ext {
					merged[k] = v
				}
				r.ExtensionsInfo = merged
			}
		}
		card.Remotes[i] = r
	}
	return card
}

// validateServerCard panics when the configured card breaks the schema.
// The derived remote URL is not known yet, so it validates with a
// placeholder that always passes the URL pattern.
func (s *Server) validateServerCard() {
	card := s.buildServerCard("https://placeholder.invalid/mcp", nil)
	if err := card.Validate(); err != nil {
		panic("mcpkit: WithServerCard: " + err.Error())
	}
}

// cardVersions is what the endpoint accepts in the given mode. A
// stateless-only endpoint advertises what server/discover advertises, and an
// endpoint that also speaks the session wire advertises every version
// initialize can negotiate.
func (s *Server) cardVersions(mode stateless.Mode) []string {
	if mode == stateless.ModeStateless {
		return s.dispatcher.discoverResult().SupportedVersions
	}
	return s.dispatcher.protocolVersions()
}

// requestStreamableURL rebuilds the streamable URL the request reached the
// card through. It trusts X-Forwarded-Proto for the scheme only, since a TLS
// proxy in front is the common case and the card is public either way.
func requestStreamableURL(r *http.Request, prefix string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + prefix
}

// setCardCORS writes the four headers the discovery doc makes a MUST.
func setCardCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET")
	h.Set("Access-Control-Allow-Headers", "Content-Type, If-None-Match")
	h.Set("Access-Control-Expose-Headers", "ETag")
}

// serverCardHandler serves the card at <prefix>/server-card.
func (s *Server) serverCardHandler(prefix string, mode stateless.Mode) http.Handler {
	versions := s.cardVersions(mode)
	maxAge := s.options.serverCard.maxAge
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		setCardCORS(h)
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet, http.MethodHead:
		default:
			h.Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		card := s.buildServerCard(requestStreamableURL(r, prefix), versions)
		body, err := json.Marshal(card)
		if err != nil {
			http.Error(w, "server card encoding failed", http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`
		h.Set("ETag", etag)
		if maxAge > 0 {
			h.Set("Cache-Control", "public, max-age="+strconv.Itoa(int(maxAge/time.Second)))
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		h.Add("Vary", "Host")
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", core.ServerCardMediaType)
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}

// etagMatches applies If-None-Match's weak comparison: "*" matches, and a
// W/ prefix on either side is ignored.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}
