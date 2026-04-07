package mcpkit

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	gohttp "github.com/panyam/servicekit/http"
)

// Server is an MCP server that can run over multiple transports.
type Server struct {
	dispatcher         *Dispatcher
	options            serverOptions
	mu                 sync.Mutex
	sessionClosers     []sessionCloser
	allSessionClosers  []func()
}

type serverOptions struct {
	listen        string
	bearerToken   string
	toolTimeout   time.Duration
	allowedRoots  []string
	authValidator AuthValidator
	extensions    []ExtensionProvider
	middleware    []Middleware
	requestLogger *log.Logger // HTTP-level request/response logging
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

// WithRequestLogging enables HTTP-level request/response logging on the server.
// Logs every incoming HTTP request with method, path, headers (Mcp-Session-Id,
// Accept, Authorization presence), and the response status code and content-type.
// This is transport-level logging — for JSON-RPC dispatch-level logging, use
// WithMiddleware(LoggingMiddleware(logger)).
//
// Example:
//
//	srv := mcpkit.NewServer(info, mcpkit.WithRequestLogging(log.Default()))
func WithRequestLogging(logger *log.Logger) Option {
	return func(o *serverOptions) {
		if logger == nil {
			logger = log.Default()
		}
		o.requestLogger = logger
	}
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

	// Build the terminal handler: dispatch with optional tool timeout.
	handler := MiddlewareFunc(func(ctx context.Context, req *Request) *Response {
		if s.options.toolTimeout > 0 && req.Method == "tools/call" {
			tctx, cancel := context.WithTimeout(ctx, s.options.toolTimeout)
			defer cancel()
			return d.Dispatch(tctx, req)
		}
		return d.Dispatch(ctx, req)
	})

	// Wrap with user middleware (reverse order: first registered = outermost).
	for i := len(s.options.middleware) - 1; i >= 0; i-- {
		next := handler
		mw := s.options.middleware[i]
		handler = func(ctx context.Context, req *Request) *Response {
			return mw(ctx, req, next)
		}
	}

	return handler(ctx, req)
}

// newSession creates a per-session Dispatcher clone with fresh session state.
func (s *Server) newSession() *Dispatcher {
	return s.dispatcher.newSession()
}

// sessionCloser is called to close sessions on a transport.
type sessionCloser func(id string) bool

// sessionClosers tracks active transports for session management.
var _ = sessionCloser(nil) // type check

// CloseSession terminates an active session by ID across all transports.
// Returns true if the session was found and closed.
func (s *Server) CloseSession(id string) bool {
	s.mu.Lock()
	closers := make([]sessionCloser, len(s.sessionClosers))
	copy(closers, s.sessionClosers)
	s.mu.Unlock()

	for _, closer := range closers {
		if closer(id) {
			return true
		}
	}
	return false
}

// CloseAllSessions terminates all active sessions across all transports.
func (s *Server) CloseAllSessions() {
	s.mu.Lock()
	allClosers := make([]func(), len(s.allSessionClosers))
	copy(allClosers, s.allSessionClosers)
	s.mu.Unlock()

	for _, closer := range allClosers {
		closer()
	}
}

// registerTransportSessions registers a transport's session management callbacks.
// Called by transports during Handler() creation.
func (s *Server) registerTransportSessions(closeOne sessionCloser, closeAll func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionClosers = append(s.sessionClosers, closeOne)
	s.allSessionClosers = append(s.allSessionClosers, closeAll)
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

	var handler http.Handler

	// SSE only (default, backward compatible)
	if cfg.sse && !cfg.streamableHTTP {
		sseT := newSSETransport(s, opts...)
		s.registerTransportSessions(sseT.closeSession, sseT.closeAllSessions)
		handler = sseT.handler()
	} else if cfg.streamableHTTP && !cfg.sse {
		// Streamable HTTP only
		stT := newStreamableTransport(s, cfg)
		s.registerTransportSessions(stT.closeSession, stT.closeAllSessions)
		handler = stT.handler()
	} else {
		// Both enabled: SSE at /sse + /message, Streamable HTTP at base prefix
		mux := http.NewServeMux()
		if cfg.sse {
			sseT := newSSETransport(s, opts...)
			s.registerTransportSessions(sseT.closeSession, sseT.closeAllSessions)
			sseT.mountOn(mux, prefix)
		}
		if cfg.streamableHTTP {
			stT := newStreamableTransport(s, cfg)
			s.registerTransportSessions(stT.closeSession, stT.closeAllSessions)
			mux.HandleFunc(prefix, stT.handleRoot)
		}
		handler = mux
	}

	// Wrap with HTTP-level request logging if configured
	if s.options.requestLogger != nil {
		handler = requestLoggingHandler(s.options.requestLogger, handler)
	}

	return handler
}

// requestLoggingHandler wraps an http.Handler with request/response logging.
// Logs method, path, key headers, and response status for every HTTP request.
func requestLoggingHandler(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log request
		sessionID := r.Header.Get("Mcp-Session-Id")
		accept := r.Header.Get("Accept")
		hasAuth := r.Header.Get("Authorization") != ""
		logger.Printf("[http] → %s %s session=%q accept=%q auth=%v",
			r.Method, r.URL.Path, sessionID, accept, hasAuth)

		// Wrap ResponseWriter to capture status code
		rw := &statusCapture{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)

		// Log response
		ct := rw.Header().Get("Content-Type")
		logger.Printf("[http] ← %d %s content-type=%q",
			rw.status, r.URL.Path, ct)
	})
}

// statusCapture wraps http.ResponseWriter to capture the status code.
// It preserves the http.Flusher interface so SSE streaming still works.
type statusCapture struct {
	http.ResponseWriter
	status int
}

func (w *statusCapture) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter if it implements http.Flusher.
// This is critical — without it, SSE streaming breaks because the flusher check fails.
func (w *statusCapture) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Run is a convenience entry point that starts the server with Streamable HTTP
// on the given address. It is equivalent to:
//
//	srv.ListenAndServe(mcpkit.WithStreamableHTTP(true))
//
// with the address set via WithListen. For more control over transport options,
// use ListenAndServe directly.
//
// Example:
//
//	srv := mcpkit.NewServer(mcpkit.ServerInfo{Name: "my-server", Version: "1.0"})
//	srv.RegisterTool(def, handler)
//	srv.Run(":8787")
func (s *Server) Run(addr string, opts ...TransportOption) error {
	if addr != "" {
		s.options.listen = addr
	}
	// Default to Streamable HTTP if no transport option explicitly set
	hasTransport := false
	for _, opt := range opts {
		// Check if any transport option was provided by applying to a temp config
		tc := transportConfig{}
		opt(&tc)
		if tc.streamableHTTP || tc.sse {
			hasTransport = true
			break
		}
	}
	if !hasTransport {
		opts = append([]TransportOption{WithStreamableHTTP(true)}, opts...)
	}
	return s.ListenAndServe(opts...)
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
	stateless      bool     // stateless mode: no sessions, fresh dispatcher per request
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

// WithStateless enables stateless mode for the Streamable HTTP transport.
// In stateless mode, every request gets a fresh Dispatcher — no session storage,
// no Mcp-Session-Id header, no state carried across requests. The initialize
// handshake is auto-performed per request.
//
// Use for simple tool servers that don't need session state (e.g., single-tool
// APIs, serverless functions, CLI wrappers).
func WithStateless(enabled bool) TransportOption {
	return func(c *transportConfig) { c.stateless = enabled }
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
