package mcpkit

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"
)

// Server is an MCP server that can run over multiple transports.
type Server struct {
	dispatcher *Dispatcher
	options    serverOptions
}

type serverOptions struct {
	listen       string
	bearerToken  string
	toolTimeout  time.Duration
	allowedRoots []string
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
	if s.options.toolTimeout > 0 && req.Method == "tools/call" {
		tctx, cancel := context.WithTimeout(ctx, s.options.toolTimeout)
		defer cancel()
		return s.dispatcher.Dispatch(tctx, req)
	}
	return s.dispatcher.Dispatch(ctx, req)
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
