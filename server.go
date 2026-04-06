package mcpkit

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
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
	extensions    []ExtensionProvider
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

// WithExtension registers a protocol extension that will be advertised
// in the initialize response. Extensions declare their ID, spec version,
// and stability level.
func WithExtension(ext ExtensionProvider) Option {
	return func(o *serverOptions) { o.extensions = append(o.extensions, ext) }
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
	// Register extensions on the dispatcher so they appear in initialize response
	for _, ext := range s.options.extensions {
		e := ext.Extension()
		s.dispatcher.extensions[e.ID] = e
	}
	return s
}

// RegisterTool adds a tool to the server.
func (s *Server) RegisterTool(def ToolDef, handler ToolHandler) {
	s.dispatcher.RegisterTool(def, handler)
}

// RegisterResource adds a resource to the server.
func (s *Server) RegisterResource(def ResourceDef, handler ResourceHandler) {
	s.dispatcher.RegisterResource(def, handler)
}

// RegisterResourceTemplate adds a URI template resource to the server.
func (s *Server) RegisterResourceTemplate(def ResourceTemplate, handler TemplateHandler) {
	s.dispatcher.RegisterResourceTemplate(def, handler)
}

// RegisterPrompt adds a prompt to the server.
func (s *Server) RegisterPrompt(def PromptDef, handler PromptHandler) {
	s.dispatcher.RegisterPrompt(def, handler)
}

// RegisterExperimentalTool registers a tool marked as experimental via annotations.
func (s *Server) RegisterExperimentalTool(def ToolDef, handler ToolHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterTool(def, handler)
}

// RegisterExperimentalResource registers a resource marked as experimental via annotations.
func (s *Server) RegisterExperimentalResource(def ResourceDef, handler ResourceHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterResource(def, handler)
}

// RegisterExperimentalPrompt registers a prompt marked as experimental via annotations.
func (s *Server) RegisterExperimentalPrompt(def PromptDef, handler PromptHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterPrompt(def, handler)
}

// RegisterCompletion registers a completion handler for argument autocompletion.
// refType is "ref/prompt" or "ref/resource". name is the prompt name or resource URI template.
func (s *Server) RegisterCompletion(refType, name string, handler CompletionHandler) {
	s.dispatcher.RegisterCompletion(refType, name, handler)
}

// Dispatch routes a JSON-RPC request through the server's dispatch layer.
func (s *Server) Dispatch(ctx context.Context, req *Request) *Response {
	return s.dispatchWith(s.dispatcher, ctx, nil, req)
}

// dispatchWith routes a request through a specific dispatcher with server-level
// middleware (e.g. tool timeout). Used by transports to dispatch on per-session
// dispatchers. The claims parameter carries the authenticated identity from CheckAuth.
func (s *Server) dispatchWith(d *Dispatcher, ctx context.Context, claims *Claims, req *Request) *Response {
	return s.dispatchWithNotify(d, ctx, claims, d.notifyFunc, req)
}

// dispatchWithNotify is like dispatchWith but accepts an explicit NotifyFunc.
// Used by handlePostSSE to pass a request-scoped notify function that writes
// to the current SSE stream, avoiding races on d.notifyFunc when concurrent
// SSE-streaming POSTs share the same session dispatcher.
func (s *Server) dispatchWithNotify(d *Dispatcher, ctx context.Context, claims *Claims, notify NotifyFunc, req *Request) *Response {
	// Inject session context so tool handlers can send notifications (logging, progress, etc.)
	// and access authenticated claims.
	ctx = contextWithSession(ctx, notify, &d.logLevel, claims)

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

// Handler returns an http.Handler implementing MCP transports.
// By default, only the legacy SSE transport is enabled. Use WithStreamableHTTP(true)
// to enable the Streamable HTTP transport (MCP 2025-03-26).
// Both transports can be enabled simultaneously for backward compatibility.
func (s *Server) Handler(opts ...TransportOption) http.Handler {
	cfg := defaultTransportConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	prefix := strings.TrimRight(cfg.prefix, "/")

	// SSE only (default, backward compatible)
	if cfg.sse && !cfg.streamableHTTP {
		return newSSETransport(s, opts...).handler()
	}

	// Streamable HTTP only
	if cfg.streamableHTTP && !cfg.sse {
		return newStreamableTransport(s, cfg).handler()
	}

	// Both enabled: SSE at /sse + /message, Streamable HTTP at base prefix
	mux := http.NewServeMux()
	if cfg.sse {
		sseT := newSSETransport(s, opts...)
		sseT.mountOn(mux, prefix)
	}
	if cfg.streamableHTTP {
		stT := newStreamableTransport(s, cfg)
		mux.HandleFunc(prefix, stT.handleRoot)
	}
	return mux
}

// ListenAndServe starts the HTTP transport(s) with graceful shutdown support.
// On SIGTERM/SIGINT it stops accepting new connections, closes active sessions,
// drains in-flight requests, and exits.
func (s *Server) ListenAndServe(opts ...TransportOption) error {
	cfg := defaultTransportConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	addr := s.options.listen
	if addr == "" {
		addr = ":8080"
	}

	handler := s.Handler(opts...)
	var shutdownFns []func()

	// Collect shutdown callbacks from active transports
	if cfg.sse {
		// SSE hub cleanup is handled internally by the SSE transport
		// through the handler's SSEHub.CloseAll
	}

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		WriteTimeout: 0, // SSE requires no write timeout on long-lived connections
	}

	var gracefulOpts []gohttp.GracefulOption
	for _, fn := range shutdownFns {
		gracefulOpts = append(gracefulOpts, gohttp.WithOnShutdown(fn))
	}
	return gohttp.ListenAndServeGraceful(httpSrv, gracefulOpts...)
}

// TransportOption configures the HTTP transports.
type TransportOption func(*transportConfig)

type transportConfig struct {
	prefix          string        // URL path prefix (default "/mcp")
	publicURL       string        // public base URL for reverse proxy deployments
	maxSessions     int           // max concurrent sessions (0 = unlimited)
	keepalivePeriod time.Duration // SSE keepalive interval (default 30s)
	allowedOrigins []string // allowed Origin values for DNS rebinding protection
	streamableHTTP bool     // enable Streamable HTTP transport
	sse            bool     // enable legacy SSE transport
}

func defaultTransportConfig() transportConfig {
	return transportConfig{
		prefix:          "/mcp",
		keepalivePeriod: 30 * time.Second,
		sse:             true,
		streamableHTTP:  false,
	}
}

// WithPrefix sets the URL path prefix for transport endpoints.
func WithPrefix(p string) TransportOption {
	return func(c *transportConfig) { c.prefix = p }
}

// WithPublicURL sets the public base URL used in the SSE endpoint event.
// Use this when the server is behind a reverse proxy.
func WithPublicURL(u string) TransportOption {
	return func(c *transportConfig) { c.publicURL = u }
}

// WithMaxSessions limits the number of concurrent sessions.
func WithMaxSessions(n int) TransportOption {
	return func(c *transportConfig) { c.maxSessions = n }
}

// WithKeepalivePeriod sets the interval for SSE keepalive comments.
func WithKeepalivePeriod(d time.Duration) TransportOption {
	return func(c *transportConfig) { c.keepalivePeriod = d }
}

// WithAllowedOrigins sets the allowed Origin header values for DNS rebinding protection.
// When empty (default), only localhost origins are accepted.
func WithAllowedOrigins(origins ...string) TransportOption {
	return func(c *transportConfig) { c.allowedOrigins = origins }
}

// WithStreamableHTTP enables or disables the Streamable HTTP transport (MCP 2025-03-26).
func WithStreamableHTTP(enabled bool) TransportOption {
	return func(c *transportConfig) { c.streamableHTTP = enabled }
}

// WithSSE enables or disables the legacy SSE transport (MCP 2024-11-05).
func WithSSE(enabled bool) TransportOption {
	return func(c *transportConfig) { c.sse = enabled }
}

// CheckAuth validates an HTTP request against the server's auth configuration.
// Returns the authenticated claims (if the validator provides them) and any error.
// Returns (nil, nil) if no auth is configured.
func (s *Server) CheckAuth(r *http.Request) (*Claims, error) {
	if s.options.authValidator == nil {
		return nil, nil
	}
	if err := s.options.authValidator.Validate(r); err != nil {
		return nil, err
	}
	if cp, ok := s.options.authValidator.(ClaimsProvider); ok {
		return cp.Claims(r), nil
	}
	return nil, nil
}

// writeAuthError writes an authentication/authorization error to the response.
// If the error is an *AuthError with a WWWAuthenticate field, the WWW-Authenticate
// header is set. Used by both transports for consistent error responses.
func writeAuthError(w http.ResponseWriter, err error) {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		if authErr.WWWAuthenticate != "" {
			w.Header().Set("WWW-Authenticate", authErr.WWWAuthenticate)
		}
		http.Error(w, authErr.Message, authErr.Code)
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// bearerTokenValidator uses constant-time comparison.
type bearerTokenValidator struct {
	token string
}

func (v *bearerTokenValidator) Validate(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
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
	Code            int
	Message         string
	WWWAuthenticate string // optional WWW-Authenticate header value
}

func (e *AuthError) Error() string { return e.Message }
