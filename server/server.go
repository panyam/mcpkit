package server

import (
	core "github.com/panyam/mcpkit/core"
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
	dispatcher        *Dispatcher
	options           serverOptions
	mu                sync.Mutex
	sessionClosers      []sessionCloser
	allSessionClosers   []func()
	sessionBroadcasters []func(method string, params any)
	subRegistry         *subscriptionRegistry // nil when subscriptions not enabled
}

type serverOptions struct {
	listen               string
	bearerToken          string
	toolTimeout          time.Duration
	allowedRoots         []string
	authValidator        core.AuthValidator
	extensions           []core.ExtensionProvider
	middleware           []Middleware
	requestLogger        *log.Logger // HTTP-level request/response logging
	subscriptionsEnabled bool        // enable resources/subscribe and resources/unsubscribe
	errorHandler         ErrorHandler // optional out-of-band error callback
	contentChunkMethod   string       // custom notification method for streaming content (empty = default)
	onRootsChanged       func([]core.Root) // optional callback when client sends roots/list_changed
	skipSchemaValidation bool              // WithSchemaValidation(false) disables call-time validation
}

// ErrorHandler receives out-of-band errors that aren't returned to a
// specific client request — session lifecycle errors, transport failures,
// keepalive timeouts. Implement for observability, alerting, or diagnostics.
//
// Embed [BaseErrorHandler] to only override the methods you care about.
type ErrorHandler interface {
	// OnSessionExpire is called when a session is terminated due to idle
	// timeout, keepalive failure, or explicit close.
	OnSessionExpire(sessionID string, reason error)

	// OnTransportError is called for transport-level errors that don't
	// result in a client response (connection drops, body read failures).
	OnTransportError(err error)

	// OnKeepaliveFailure is called each time a keepalive ping fails.
	// consecutiveFailures is the current streak count.
	OnKeepaliveFailure(sessionID string, consecutiveFailures int)
}

// BaseErrorHandler provides no-op defaults for all [ErrorHandler] methods.
// Embed this in your implementation to only override specific methods:
//
//	type MyHandler struct {
//	    server.BaseErrorHandler
//	    logger *slog.Logger
//	}
//	func (h *MyHandler) OnSessionExpire(id string, err error) {
//	    h.logger.Error("session expired", "id", id, "err", err)
//	}
type BaseErrorHandler struct{}

func (BaseErrorHandler) OnSessionExpire(string, error)  {}
func (BaseErrorHandler) OnTransportError(error)         {}
func (BaseErrorHandler) OnKeepaliveFailure(string, int) {}

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
func WithAuth(v core.AuthValidator) Option {
	return func(o *serverOptions) { o.authValidator = v }
}

// WithExtension registers a protocol extension that will be advertised
// in the initialize response. Extensions declare their ID, spec version,
// and stability level.
func WithExtension(ext core.ExtensionProvider) Option {
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

// WithErrorHandler sets a callback for out-of-band errors (session lifecycle,
// transport failures, keepalive timeouts). When not set, these errors are
// only logged via log.Printf.
func WithErrorHandler(h ErrorHandler) Option {
	return func(o *serverOptions) { o.errorHandler = h }
}

// WithOnRootsChanged sets a callback invoked when the client sends
// notifications/roots/list_changed. The callback receives the root list
// (which may be empty if the client hasn't provided roots yet).
// Use this to dynamically update allowed roots or trigger re-validation.
func WithOnRootsChanged(fn func([]core.Root)) Option {
	return func(o *serverOptions) { o.onRootsChanged = fn }
}

// WithContentChunkMethod sets a custom notification method name for streaming
// tool content chunks. When not set, defaults to core.DefaultContentChunkMethod
// ("notifications/tools/content_chunk"). Use this if clients expect a different
// method name for streaming content delivery.
func WithContentChunkMethod(method string) Option {
	return func(o *serverOptions) { o.contentChunkMethod = method }
}

// WithSubscriptions enables resource subscription support (resources/subscribe,
// resources/unsubscribe, and notifications/resources/updated). When enabled,
// the server advertises "subscribe": true in the resources capability and
// accepts subscription requests from clients.
//
// Use Server.NotifyResourceUpdated(uri) to push change notifications to all
// sessions that have subscribed to the given URI.
func WithSubscriptions() Option {
	return func(o *serverOptions) { o.subscriptionsEnabled = true }
}

// WithToolTimeout sets the maximum duration for tool execution.
func WithToolTimeout(d time.Duration) Option {
	return func(o *serverOptions) { o.toolTimeout = d }
}

// WithSchemaValidation toggles call-time JSON Schema validation of tool
// arguments and prompt arguments against their declared schemas. When
// enabled (the default), the dispatcher validates incoming arguments
// before the handler is invoked and returns -32602 Invalid Params with
// structured error data on failure. When disabled, handlers receive
// arguments unchecked and are responsible for validation themselves.
//
// Registration-time schema compilation is not affected by this option —
// malformed schemas still fail fast at RegisterTool/RegisterPrompt time.
// This option only controls whether the compiled schemas are applied to
// incoming requests.
//
// Example — opt out of validation:
//
//	srv := mcpkit.NewServer(info, mcpkit.WithSchemaValidation(false))
func WithSchemaValidation(enabled bool) Option {
	return func(o *serverOptions) { o.skipSchemaValidation = !enabled }
}

// WithAllowedRoots restricts tool cwd to the given directory prefixes.
func WithAllowedRoots(roots ...string) Option {
	return func(o *serverOptions) { o.allowedRoots = roots }
}

// NewServer creates an MCP server with the given identity and options.
func NewServer(info core.ServerInfo, opts ...Option) *Server {
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
	// Propagate schema validation opt-out to the dispatcher so per-session
	// clones inherit it via newSession().
	s.dispatcher.skipSchemaValidation = s.options.skipSchemaValidation
	// Wire registry change notifications to Server.Broadcast so that
	// dynamic adds/removes automatically notify all connected sessions.
	s.dispatcher.Reg.OnChange = func(method string) {
		s.Broadcast(method, nil)
	}
	// Wire roots callback
	if s.options.onRootsChanged != nil {
		s.dispatcher.onRootsChanged = s.options.onRootsChanged
	}
	// Initialize subscription support if enabled
	if s.options.subscriptionsEnabled {
		s.subRegistry = &subscriptionRegistry{
			subscribers: make(map[string]map[string]*Dispatcher),
		}
		s.dispatcher.subscriptionsEnabled = true
		s.dispatcher.subManager = s.subRegistry
	}
	return s
}

// RegisterTool adds a tool to the server.
func (s *Server) RegisterTool(def core.ToolDef, handler core.ToolHandler) {
	s.dispatcher.RegisterTool(def, handler)
}

// RegisterResource adds a resource to the server.
func (s *Server) RegisterResource(def core.ResourceDef, handler core.ResourceHandler) {
	s.dispatcher.RegisterResource(def, handler)
}

// RegisterResourceTemplate adds a URI template resource to the server.
func (s *Server) RegisterResourceTemplate(def core.ResourceTemplate, handler core.TemplateHandler) {
	s.dispatcher.RegisterResourceTemplate(def, handler)
}

// validateExtensionRefs iterates registered extensions and calls ValidateRefs
// on any that implement core.RefValidator. Logs warnings for unresolvable refs.
func (s *Server) validateExtensionRefs() {
	logger := s.options.requestLogger
	if logger == nil {
		logger = log.Default()
	}

	// Collect tool defs, resource URIs, and template URIs under read lock
	reg := s.dispatcher.Reg
	reg.mu.RLock()
	tools := make([]core.ToolDef, 0, len(reg.toolOrder))
	for _, name := range reg.toolOrder {
		if entry, ok := reg.tools[name]; ok {
			tools = append(tools, entry.def)
		}
	}
	resourceURIs := make([]string, len(reg.resourceOrder))
	copy(resourceURIs, reg.resourceOrder)
	templateURIs := make([]string, len(reg.templateOrder))
	copy(templateURIs, reg.templateOrder)
	reg.mu.RUnlock()

	for _, ext := range s.options.extensions {
		if rv, ok := ext.(core.RefValidator); ok {
			for _, warning := range rv.ValidateRefs(tools, resourceURIs, templateURIs) {
				logger.Printf("mcpkit: %s", warning)
			}
		}
	}
}

// RegisterPrompt adds a prompt to the server.
func (s *Server) RegisterPrompt(def core.PromptDef, handler core.PromptHandler) {
	s.dispatcher.RegisterPrompt(def, handler)
}

// RegisterExperimentalTool registers a tool marked as experimental via annotations.
func (s *Server) RegisterExperimentalTool(def core.ToolDef, handler core.ToolHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterTool(def, handler)
}

// RegisterExperimentalResource registers a resource marked as experimental via annotations.
func (s *Server) RegisterExperimentalResource(def core.ResourceDef, handler core.ResourceHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterResource(def, handler)
}

// RegisterExperimentalPrompt registers a prompt marked as experimental via annotations.
func (s *Server) RegisterExperimentalPrompt(def core.PromptDef, handler core.PromptHandler) {
	if def.Annotations == nil {
		def.Annotations = make(map[string]any)
	}
	def.Annotations["experimental"] = true
	s.RegisterPrompt(def, handler)
}

// RegisterCompletion registers a completion handler for argument autocompletion.
// refType is "ref/prompt" or "ref/resource". name is the prompt name or resource URI template.
func (s *Server) RegisterCompletion(refType, name string, handler core.CompletionHandler) {
	s.dispatcher.RegisterCompletion(refType, name, handler)
}

// Registry returns the server's shared registry. All session dispatchers
// share this registry, so changes (AddTool, RemoveTool, etc.) are
// immediately visible to every session. When the server has active
// sessions, mutations automatically broadcast the appropriate
// notifications/*/list_changed notification.
func (s *Server) Registry() *Registry {
	return s.dispatcher.Reg
}

// Dispatch routes a JSON-RPC request through the server's dispatch layer.
func (s *Server) Dispatch(ctx context.Context, req *core.Request) *core.Response {
	return s.dispatchWith(s.dispatcher, ctx, nil, req)
}

// dispatchWith routes a request through a specific dispatcher with server-level
// middleware (e.g. tool timeout). Used by transports to dispatch on per-session
// dispatchers. The claims parameter carries the authenticated identity from CheckAuth.
func (s *Server) dispatchWith(d *Dispatcher, ctx context.Context, claims *core.Claims, req *core.Request) *core.Response {
	return s.dispatchWithNotify(d, ctx, claims, d.getNotifyFunc(), req)
}

// dispatchWithNotify is like dispatchWith but accepts an explicit core.NotifyFunc.
// Used by handlePostSSE to pass a request-scoped notify function that writes
// to the current SSE stream, avoiding races on d.notifyFunc when concurrent
// SSE-streaming POSTs share the same session dispatcher.
func (s *Server) dispatchWithNotify(d *Dispatcher, ctx context.Context, claims *core.Claims, notify core.NotifyFunc, req *core.Request) *core.Response {
	return s.dispatchWithNotifyAndRequest(d, ctx, claims, notify, nil, req)
}

// dispatchWithNotifyAndRequest is the full dispatch entry point that accepts both
// a core.NotifyFunc and core.RequestFunc. Used by transports that support server-to-client requests.
func (s *Server) dispatchWithNotifyAndRequest(d *Dispatcher, ctx context.Context, claims *core.Claims, notify core.NotifyFunc, request core.RequestFunc, req *core.Request) *core.Response {
	// Inject session context so tool handlers can send notifications, requests,
	// and access authenticated claims and client capabilities.
	ctx = core.ContextWithSession(ctx, notify, request, &d.logLevel, &d.clientCaps, claims)

	// Inject custom content chunk method if configured.
	if s.options.contentChunkMethod != "" {
		ctx = core.WithContentChunkMethod(ctx, s.options.contentChunkMethod)
	}

	// Build the terminal handler: dispatch with optional tool timeout.
	handler := MiddlewareFunc(func(ctx context.Context, req *core.Request) *core.Response {
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
		handler = func(ctx context.Context, req *core.Request) *core.Response {
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

// Broadcast sends a JSON-RPC notification to ALL connected sessions across
// all transports. Unlike NotifyResourceUpdated (which targets only sessions
// that have called resources/subscribe for a specific URI), Broadcast fans
// out unconditionally to every session with push capability.
//
// Typical use cases: notifications/tools/list_changed,
// notifications/prompts/list_changed, or application-level broadcasts.
//
// Safe to call from any goroutine. No-op if no sessions are connected.
// Sessions without push capability (e.g., Streamable HTTP without GET SSE
// stream) are silently skipped. Does not hold the server mutex during
// notification delivery.
//
// Note: only reaches sessions registered through Handler() (SSE and
// Streamable HTTP transports). In-process transports manage their own
// notification delivery via WithNotificationHandler.
//
// Example:
//
//	// After registering a new tool at runtime:
//	srv.Broadcast("notifications/tools/list_changed", nil)
func (s *Server) Broadcast(method string, params any) {
	s.mu.Lock()
	broadcasters := make([]func(string, any), len(s.sessionBroadcasters))
	copy(broadcasters, s.sessionBroadcasters)
	s.mu.Unlock()

	for _, bc := range broadcasters {
		bc(method, params)
	}
}

// registerTransportSessions registers a transport's session management callbacks.
// Called by transports during Handler() creation.
func (s *Server) registerTransportSessions(closeOne sessionCloser, closeAll func(), broadcast func(method string, params any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionClosers = append(s.sessionClosers, closeOne)
	s.allSessionClosers = append(s.allSessionClosers, closeAll)
	s.sessionBroadcasters = append(s.sessionBroadcasters, broadcast)
}

// Handler returns an http.Handler implementing MCP transports.
// By default, only the legacy SSE transport is enabled. Use WithStreamableHTTP(true)
// to enable the Streamable HTTP transport (MCP 2025-03-26).
// Both transports can be enabled simultaneously for backward compatibility.
func (s *Server) Handler(opts ...TransportOption) http.Handler {
	// Let extensions validate tool-to-resource references at startup
	s.validateExtensionRefs()

	cfg := defaultTransportConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	prefix := strings.TrimRight(cfg.prefix, "/")

	var handler http.Handler

	// SSE only (default, backward compatible)
	if cfg.sse && !cfg.streamableHTTP {
		sseT := newSSETransport(s, opts...)
		s.registerTransportSessions(sseT.closeSession, sseT.closeAllSessions, sseT.broadcast)
		handler = sseT.handler()
	} else if cfg.streamableHTTP && !cfg.sse {
		// Streamable HTTP only
		stT := newStreamableTransport(s, cfg)
		s.registerTransportSessions(stT.closeSession, stT.closeAllSessions, stT.broadcast)
		handler = stT.handler()
	} else {
		// Both enabled: SSE at /sse + /message, Streamable HTTP at base prefix
		mux := http.NewServeMux()
		if cfg.sse {
			sseT := newSSETransport(s, opts...)
			s.registerTransportSessions(sseT.closeSession, sseT.closeAllSessions, sseT.broadcast)
			sseT.mountOn(mux, prefix)
		}
		if cfg.streamableHTTP {
			stT := newStreamableTransport(s, cfg)
			s.registerTransportSessions(stT.closeSession, stT.closeAllSessions, stT.broadcast)
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
	stateless      bool          // stateless mode: no sessions, fresh dispatcher per request
	sessionTimeout time.Duration // idle timeout for Streamable HTTP sessions (0 = no timeout)
	eventStore     gohttp.EventStore // optional: persists SSE events for Last-Event-ID replay
	keepaliveInterval time.Duration  // 0 = disabled; interval for JSON-RPC ping requests
	keepaliveMaxFails int            // max consecutive ping failures before session cleanup (default 3)
	sseGracePeriod    time.Duration  // 0 = immediate cleanup on SSE disconnect (backward compat)
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

// WithSessionTimeout sets the idle timeout for Streamable HTTP sessions.
// Sessions that receive no POST requests for this duration are automatically
// expired and cleaned up. Active requests (in-flight tool calls, open GET SSE
// streams) pause the timer so sessions are never closed mid-execution.
// Default is 0 (no timeout — sessions persist until explicit DELETE or server restart).
func WithSessionTimeout(d time.Duration) TransportOption {
	return func(c *transportConfig) { c.sessionTimeout = d }
}

// WithSSEGracePeriod sets the grace period for SSE sessions after disconnect.
// When an SSE connection closes, the session stays alive for this duration.
// If the client reconnects with the same session ID (via ?sessionId= query
// param), it resumes the existing session and replays missed events via
// Last-Event-ID. Requires WithEventStore for event replay.
//
// Default is 0 (no grace period — sessions die immediately on disconnect,
// backward compatible with pre-v0.1.17 behavior).
//
// Security: reconnection requires the same auth principal as the original
// session. Session IDs are cryptographically random.
func WithSSEGracePeriod(d time.Duration) TransportOption {
	return func(c *transportConfig) { c.sseGracePeriod = d }
}

// WithEventStore sets an optional EventStore for SSE event persistence.
// When configured, all SSE events (GET SSE stream notifications and POST SSE
// response events) are stored with unique IDs. Clients that reconnect with a
// Last-Event-ID header receive missed events via replay.
//
// Pass nil to disable (default). Use gohttp.NewMemoryEventStore(maxPerStream)
// for an in-memory implementation.
func WithEventStore(store gohttp.EventStore) TransportOption {
	return func(c *transportConfig) { c.eventStore = store }
}

// WithKeepalive enables application-level keepalive pings for session liveness
// detection. When enabled, the server periodically sends JSON-RPC ping requests
// to clients via their GET SSE stream. If maxFailures consecutive pings fail
// (timeout or error), the session is expired.
//
// interval controls how often pings are sent (e.g., 30*time.Second).
// maxFailures is the threshold before session cleanup (e.g., 3).
// Set interval to 0 to disable (default).
func WithKeepalive(interval time.Duration, maxFailures int) TransportOption {
	return func(c *transportConfig) {
		c.keepaliveInterval = interval
		c.keepaliveMaxFails = maxFailures
	}
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

// notifyError calls the configured ErrorHandler method if one is set.
// Safe to call when errorHandler is nil (no-op).
func (s *Server) notifySessionExpire(sessionID string, reason error) {
	if s.options.errorHandler != nil {
		s.options.errorHandler.OnSessionExpire(sessionID, reason)
	}
}

func (s *Server) notifyTransportError(err error) {
	if s.options.errorHandler != nil {
		s.options.errorHandler.OnTransportError(err)
	}
}

func (s *Server) notifyKeepaliveFailure(sessionID string, failures int) {
	if s.options.errorHandler != nil {
		s.options.errorHandler.OnKeepaliveFailure(sessionID, failures)
	}
}

// CheckAuth validates an HTTP request against the server's auth configuration.
// Returns the authenticated claims (if the validator provides them) and any error.
// Returns (nil, nil) if no auth is configured.
func (s *Server) CheckAuth(r *http.Request) (*core.Claims, error) {
	if s.options.authValidator == nil {
		return nil, nil
	}
	if err := s.options.authValidator.Validate(r); err != nil {
		return nil, err
	}
	if cp, ok := s.options.authValidator.(core.ClaimsProvider); ok {
		return cp.Claims(r), nil
	}
	return nil, nil
}

// writeAuthError writes an authentication/authorization error to the response.
// If the error is an *core.AuthError with a WWWAuthenticate field, the WWW-Authenticate
// header is set. Used by both transports for consistent error responses.
func writeAuthError(w http.ResponseWriter, err error) {
	var authErr *core.AuthError
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

var errUnauthorized = &core.AuthError{Code: http.StatusUnauthorized, Message: "unauthorized"}

// --- Resource Subscriptions ---

// subscriptionRegistry tracks which sessions have subscribed to which resource URIs.
// It lives on the Server so it can fan out notifications across all sessions.
// Thread-safe: all methods acquire the mutex.
type subscriptionRegistry struct {
	mu          sync.RWMutex
	subscribers map[string]map[string]*Dispatcher // uri → sessionID → dispatcher
}

// subscribe registers a session's interest in a resource URI.
func (r *subscriptionRegistry) subscribe(sessionID string, d *Dispatcher, uri string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	subs, ok := r.subscribers[uri]
	if !ok {
		subs = make(map[string]*Dispatcher)
		r.subscribers[uri] = subs
	}
	subs[sessionID] = d
}

// unsubscribe removes a session's subscription for a resource URI.
func (r *subscriptionRegistry) unsubscribe(sessionID, uri string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if subs, ok := r.subscribers[uri]; ok {
		delete(subs, sessionID)
		if len(subs) == 0 {
			delete(r.subscribers, uri)
		}
	}
}

// unsubscribeAll removes all subscriptions for a session (called on disconnect).
func (r *subscriptionRegistry) unsubscribeAll(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for uri, subs := range r.subscribers {
		delete(subs, sessionID)
		if len(subs) == 0 {
			delete(r.subscribers, uri)
		}
	}
}

// notify sends a notifications/resources/updated to all sessions subscribed to the URI.
// Copies the dispatcher list under read lock, then calls notifyFunc outside the lock
// to avoid holding the lock during potentially slow I/O.
func (r *subscriptionRegistry) notify(uri string) {
	r.mu.RLock()
	subs := r.subscribers[uri]
	dispatchers := make([]*Dispatcher, 0, len(subs))
	for _, d := range subs {
		dispatchers = append(dispatchers, d)
	}
	r.mu.RUnlock()

	notification := core.ResourceUpdatedNotification{URI: uri}
	for _, d := range dispatchers {
		if fn := d.getNotifyFunc(); fn != nil {
			fn("notifications/resources/updated", notification)
		}
	}
}

// NotifyResourceUpdated sends a notifications/resources/updated notification to
// all clients that have subscribed to the given resource URI. This is the
// application-facing API for triggering resource change notifications.
//
// Safe to call from any goroutine. No-op if subscriptions are not enabled or
// no clients are subscribed to the URI.
//
// Example:
//
//	// After updating config.yaml on disk:
//	srv.NotifyResourceUpdated("file:///data/config.yaml")
func (s *Server) NotifyResourceUpdated(uri string) {
	if s.subRegistry != nil {
		s.subRegistry.notify(uri)
	}
}
