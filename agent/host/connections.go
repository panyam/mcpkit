package host

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/panyam/mcpkit/agent"
)

// ConnectionsConfig is a named registry of model connections with one
// active, the shape a playground config carries so the user can switch
// models at runtime (/provider). It supersedes Config.Model for the chat
// provider when set; Model stays as the single-connection quick-start.
//
// The same registry is the seam later roles draw from: /embedder and
// /judgelm (epic 983) pick an active connection for their role from this
// one set — connections are model endpoints, roles are assignments over
// them.
type ConnectionsConfig struct {
	// Active names the connection used for the chat provider at startup.
	// Must be a key in Connections.
	Active string `json:"active"`

	// Connections maps a caller-chosen name to its endpoint + model.
	Connections map[string]ConnectionConfig `json:"connections"`
}

// ConnectionConfig is one model endpoint. Type resolves to a base URL via
// the built-in table (lmstudio / openai / ollama); BaseURL overrides it
// for anything else. Exactly one of a known Type or a BaseURL must
// resolve, or construction fails.
type ConnectionConfig struct {
	// Type names a built-in endpoint profile ("lmstudio", "openai",
	// "ollama"). Optional when BaseURL is set.
	Type string `json:"type,omitempty"`

	// BaseURL is the API root including any version prefix, e.g.
	// "http://localhost:1234/v1". Overrides Type's default.
	BaseURL string `json:"baseUrl,omitempty"`

	// Model is the model identifier sent on every request. Required.
	Model string `json:"model"`

	// APIKeyEnv names the environment variable holding the bearer key.
	// Empty means unauthenticated (local endpoints).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// ThinkingHint describes how this model delimits inline reasoning, for
	// models that emit it in the text stream (e.g. <think>…</think>).
	// Carried through the registry; the stream parser that acts on it is a
	// follow-up (issue 989) — for now it is metadata.
	ThinkingHint *ThinkingHint `json:"thinkingHint,omitempty"`
}

// ThinkingHint delimits a model's inline reasoning. An empty OpenTag
// means reasoning starts at the stream head and runs until CloseTag (the
// "no open tag" models); an empty CloseTag means the hint is inert.
type ThinkingHint struct {
	OpenTag  string `json:"openTag,omitempty"`
	CloseTag string `json:"closeTag,omitempty"`
}

// connectionBaseURLs maps a built-in Type to its default API root. Local
// runtimes and the OpenAI cloud; anything else sets BaseURL explicitly.
var connectionBaseURLs = map[string]string{
	"lmstudio": "http://localhost:1234/v1",
	"openai":   "https://api.openai.com/v1",
	"ollama":   "http://localhost:11434/v1",
}

// ProviderBuilder builds a Provider from a resolved connection. Injected
// for tests (a builder that returns StubProviders); DefaultProviderBuilder
// is the real OpenAI-compatible path.
type ProviderBuilder func(ConnectionConfig) (agent.Provider, error)

// DefaultProviderBuilder builds an OpenAI-compatible provider, resolving
// the base URL from BaseURL or the Type table and the key from APIKeyEnv.
func DefaultProviderBuilder(conn ConnectionConfig) (agent.Provider, error) {
	base, err := resolveBaseURL(conn)
	if err != nil {
		return nil, err
	}
	var key string
	if conn.APIKeyEnv != "" {
		key = os.Getenv(conn.APIKeyEnv)
	}
	return agent.NewOpenAIProvider(agent.OpenAIConfig{
		BaseURL: base,
		Model:   conn.Model,
		APIKey:  key,
	})
}

// resolveBaseURL returns the endpoint root: BaseURL wins, else the Type
// table, else an error naming the fix.
func resolveBaseURL(conn ConnectionConfig) (string, error) {
	if conn.BaseURL != "" {
		return conn.BaseURL, nil
	}
	if base, ok := connectionBaseURLs[conn.Type]; ok {
		return base, nil
	}
	if conn.Type == "" {
		return "", fmt.Errorf("host: connection needs a baseUrl or a known type (%s)", knownTypes())
	}
	return "", fmt.Errorf("host: unknown connection type %q (set baseUrl, or use one of %s)", conn.Type, knownTypes())
}

func knownTypes() string {
	ts := make([]string, 0, len(connectionBaseURLs))
	for t := range connectionBaseURLs {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
}

// ConnectionRegistry holds the named connections, the active one, and a
// cache of built providers. Safe for concurrent use: SetActive and
// provider() are the runtime-switch surface. Providers are built lazily
// and cached, so switching back to a used connection is instant.
type ConnectionRegistry struct {
	mu     sync.Mutex
	conns  map[string]ConnectionConfig
	names  []string // sorted, stable listing order
	active string
	build  ProviderBuilder
	cache  map[string]agent.Provider
}

// NewConnectionRegistry validates cfg (Active must name a connection,
// every connection must resolve a base URL) and returns the registry. A
// nil build uses DefaultProviderBuilder. Providers are not built here —
// the first Provider/SetActive call builds the active one, so a
// misconfigured non-active connection does not fail startup.
func NewConnectionRegistry(cfg *ConnectionsConfig, build ProviderBuilder) (*ConnectionRegistry, error) {
	if cfg == nil || len(cfg.Connections) == 0 {
		return nil, fmt.Errorf("host: connections config is empty")
	}
	if build == nil {
		build = DefaultProviderBuilder
	}
	names := make([]string, 0, len(cfg.Connections))
	for name, conn := range cfg.Connections {
		if conn.Model == "" {
			return nil, fmt.Errorf("host: connection %q has no model", name)
		}
		if _, err := resolveBaseURL(conn); err != nil {
			return nil, fmt.Errorf("host: connection %q: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	active := cfg.Active
	if active == "" {
		return nil, fmt.Errorf("host: connections config needs an active connection")
	}
	if _, ok := cfg.Connections[active]; !ok {
		return nil, fmt.Errorf("host: active connection %q is not defined", active)
	}
	return &ConnectionRegistry{
		conns:  cfg.Connections,
		names:  names,
		active: active,
		build:  build,
		cache:  map[string]agent.Provider{},
	}, nil
}

// Names lists the connection names in stable sorted order.
func (r *ConnectionRegistry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

// Active returns the name of the active connection.
func (r *ConnectionRegistry) Active() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// provider returns the built provider for name, building and caching on
// first use. Caller holds r.mu.
func (r *ConnectionRegistry) providerLocked(name string) (agent.Provider, error) {
	if p, ok := r.cache[name]; ok {
		return p, nil
	}
	p, err := r.build(r.conns[name])
	if err != nil {
		return nil, fmt.Errorf("host: building provider %q: %w", name, err)
	}
	r.cache[name] = p
	return p, nil
}

// ActiveProvider builds (or returns the cached) provider for the active
// connection.
func (r *ConnectionRegistry) ActiveProvider() (agent.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.providerLocked(r.active)
}

// SetActive switches the active connection and returns its provider. An
// unknown name is an error and leaves the active connection unchanged; a
// build failure likewise leaves the previous active in place, so a failed
// switch never strands the session without a provider.
func (r *ConnectionRegistry) SetActive(name string) (agent.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[name]; !ok {
		return nil, fmt.Errorf("host: unknown connection %q", name)
	}
	p, err := r.providerLocked(name)
	if err != nil {
		return nil, err
	}
	r.active = name
	return p, nil
}

// providerSwitch is an agent.Provider whose underlying provider can be
// swapped at runtime. The Runner holds one of these, so /provider never
// rebuilds the Runner; a swap takes effect on the next model call (an
// in-flight stream finishes on the provider it started with).
type providerSwitch struct {
	mu sync.RWMutex
	p  agent.Provider
}

func newProviderSwitch(p agent.Provider) *providerSwitch { return &providerSwitch{p: p} }

func (s *providerSwitch) set(p agent.Provider) {
	s.mu.Lock()
	s.p = p
	s.mu.Unlock()
}

func (s *providerSwitch) current() agent.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

func (s *providerSwitch) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return s.current().Stream(ctx, req)
}

func (s *providerSwitch) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return s.current().Generate(ctx, req)
}
