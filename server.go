package mcpkit

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	gohttp "github.com/panyam/servicekit/http"
)

// Server is an MCP server that can run over multiple transports.
type Server struct {
	dispatcher *Dispatcher
	options    serverOptions
}

type serverOptions struct {
	listen        string
	bearerToken   string
	toolTimeout   time.Duration
	allowedRoots  []string
	authValidator AuthValidator
}

// AuthValidator validates an HTTP request and returns claims on success.
type AuthValidator interface {
	Validate(r *http.Request) error
}

// Option configures a Server.
type Option func(*serverOptions)

// WithListen sets the HTTP listen address.
func WithListen(addr string) Option {
	return func(o *serverOptions) { o.listen = addr }
}

// WithBearerToken sets a static bearer token for authentication.
// Uses constant-time comparison to prevent timing attacks.
func WithBearerToken(token string) Option {
	return func(o *serverOptions) {
		o.bearerToken = token
		o.authValidator = &bearerTokenValidator{token: token}
	}
}

// WithAuth sets a custom auth validator (e.g. JWT via mcpkit/auth).
func WithAuth(v AuthValidator) Option {
	return func(o *serverOptions) { o.authValidator = v }
}

// WithToolTimeout sets the maximum duration for tool execution.
func WithToolTimeout(d time.Duration) Option {
	return func(o *serverOptions) { o.toolTimeout = d }
}

// WithAllowedRoots restricts tool cwd to the given directory prefixes.
func WithAllowedRoots(roots ...string) Option {
	return func(o *serverOptions) { o.allowedRoots = roots }
}

// NewServer creates an MCP server with the given identity and options.
func NewServer(info ServerInfo, opts ...Option) *Server {
	s := &Server{
		dispatcher: NewDispatcher(info),
	}
	for _, opt := range opts {
		opt(&s.options)
	}
	return s
}

// RegisterTool adds a tool to the server.
func (s *Server) RegisterTool(def ToolDef, handler ToolHandler) {
	s.dispatcher.RegisterTool(def, handler)
}

// Dispatch routes a JSON-RPC request through the server's dispatch layer.
func (s *Server) Dispatch(ctx context.Context, req *Request) *Response {
	return s.dispatchWith(s.dispatcher, ctx, req)
}

// dispatchWith routes a request through a specific dispatcher with server-level
// middleware (e.g. tool timeout). Used by the SSE transport to dispatch on
// per-session dispatchers.
func (s *Server) dispatchWith(d *Dispatcher, ctx context.Context, req *Request) *Response {
	if s.options.toolTimeout > 0 && req.Method == "tools/call" {
		tctx, cancel := context.WithTimeout(ctx, s.options.toolTimeout)
		defer cancel()
		return d.Dispatch(tctx, req)
	}
	return d.Dispatch(ctx, req)
}

// newSession creates a per-session Dispatcher clone with fresh session state.
func (s *Server) newSession() *Dispatcher {
	return s.dispatcher.newSession()
}

// Handler returns an http.Handler implementing the MCP HTTP+SSE transport.
// The handler serves GET {prefix}/sse for SSE connections and POST {prefix}/message
// for JSON-RPC requests.
func (s *Server) Handler(opts ...TransportOption) http.Handler {
	return newSSETransport(s, opts...).handler()
}

// ListenAndServe starts the HTTP+SSE transport with graceful shutdown support.
// On SIGTERM/SIGINT it stops accepting new connections, closes all active SSE
// sessions, drains in-flight requests, and then exits.
// The listen address comes from WithListen (default ":8080").
func (s *Server) ListenAndServe(opts ...TransportOption) error {
	t := newSSETransport(s, opts...)
	addr := s.options.listen
	if addr == "" {
		addr = ":8080"
	}
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      t.handler(),
		WriteTimeout: 0, // SSE requires no write timeout on long-lived connections
	}
	return gohttp.ListenAndServeGraceful(httpSrv,
		gohttp.WithOnShutdown(t.hub.CloseAll),
	)
}

// TransportOption configures the HTTP+SSE transport.
type TransportOption func(*transportConfig)

type transportConfig struct {
	prefix          string        // URL path prefix (default "/mcp")
	publicURL       string        // public base URL for reverse proxy deployments
	maxSessions     int           // max concurrent SSE sessions (0 = unlimited)
	keepalivePeriod time.Duration // SSE keepalive interval (default 30s)
}

func defaultTransportConfig() transportConfig {
	return transportConfig{
		prefix:          "/mcp",
		keepalivePeriod: 30 * time.Second,
	}
}

// WithPrefix sets the URL path prefix for SSE and message endpoints.
func WithPrefix(p string) TransportOption {
	return func(c *transportConfig) { c.prefix = p }
}

// WithPublicURL sets the public base URL used in the SSE endpoint event.
// Use this when the server is behind a reverse proxy.
func WithPublicURL(u string) TransportOption {
	return func(c *transportConfig) { c.publicURL = u }
}

// WithMaxSessions limits the number of concurrent SSE sessions.
func WithMaxSessions(n int) TransportOption {
	return func(c *transportConfig) { c.maxSessions = n }
}

// WithKeepalivePeriod sets the interval for SSE keepalive comments.
func WithKeepalivePeriod(d time.Duration) TransportOption {
	return func(c *transportConfig) { c.keepalivePeriod = d }
}

// CheckAuth validates an HTTP request against the server's auth configuration.
// Returns nil if no auth is configured or if the request is valid.
func (s *Server) CheckAuth(r *http.Request) error {
	if s.options.authValidator == nil {
		return nil
	}
	return s.options.authValidator.Validate(r)
}

// bearerTokenValidator uses constant-time comparison.
type bearerTokenValidator struct {
	token string
}

func (v *bearerTokenValidator) Validate(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) < len(prefix) {
		return errUnauthorized
	}
	token := auth[len(prefix):]
	if subtle.ConstantTimeCompare([]byte(token), []byte(v.token)) != 1 {
		return errUnauthorized
	}
	return nil
}

var errUnauthorized = &AuthError{Code: http.StatusUnauthorized, Message: "unauthorized"}

// AuthError is returned when authentication fails.
type AuthError struct {
	Code    int
	Message string
}

func (e *AuthError) Error() string { return e.Message }
