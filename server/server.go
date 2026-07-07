package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
	gohttp "github.com/panyam/servicekit/http"
	"golang.org/x/time/rate"
)

// Server is an MCP server that can run over multiple transports.
type Server struct {
	dispatcher        *Dispatcher
	options           serverOptions
	mu                sync.Mutex
	sessionClosers      []sessionCloser
	allSessionClosers   []func()
	sessionBroadcasters []func(ctx context.Context, method string, params any)
	subRegistry         *subscriptionRegistry // nil when subscriptions not enabled

	// metricsMiddleware wraps every dispatched JSON-RPC request when a
	// non-Noop MeterProvider was installed via WithMeterProvider. nil =
	// metrics disabled (zero-overhead path). Constructed once at NewServer
	// so instrument creation does not run on every dispatch.
	metricsMiddleware Middleware

	// sessionsActive is the Int64UpDownCounter used by the streamable
	// transport to track the "currently connected sessions" gauge. nil
	// when metrics are disabled — transports gate on nil-check before
	// calling Add. Constructed alongside metricsMiddleware.
	sessionsActive core.Int64UpDownCounter
}

type serverOptions struct {
	listen               string
	bearerToken          string
	toolTimeout          time.Duration
	allowedRoots         []string
	authValidator        core.AuthValidator
	extensions           []core.ExtensionProvider
	middleware           []Middleware
	notifyInterceptors   []NotifyInterceptor  // outgoing notification interceptors
	requestInterceptors  []RequestInterceptor // outgoing server-to-client request interceptors
	requestLogger        *log.Logger          // HTTP-level request/response logging
	subscriptionsEnabled bool        // enable resources/subscribe and resources/unsubscribe
	subscriptionCap      int         // per-session concurrent-subscription cap (0 = unlimited)
	subscriptionRate     rate.Limit  // per-session subscribe/unsubscribe rate cap (0 = unlimited)
	subscriptionBurst    int         // per-session burst alongside subscriptionRate (ignored when subscriptionRate == 0)
	subscriptionReject   SubscriptionRejectFunc // optional hook fired when a subscribe is refused (both wires)
	statelessSubCap      int         // per-scope concurrent subscriptions/listen stream cap (0 = unlimited)
	statelessSubRate     rate.Limit  // per-scope subscriptions/listen open-rate cap (0 = unlimited)
	statelessSubBurst    int         // per-scope burst alongside statelessSubRate
	statelessSubScope    StatelessSubscriptionScopeFunc // request → scope key (nil = DefaultStatelessSubscriptionScope)
	errorHandler         ErrorHandler // optional out-of-band error callback
	contentChunkMethod   string       // custom notification method for streaming content (empty = default)
	onRootsChanged       func([]core.Root) // optional callback when client sends roots/list_changed
	rootsFetchTimeout    time.Duration     // timeout for server-to-client roots/list requests (0 = default 30s)
	skipSchemaValidation bool              // WithSchemaValidation(false) disables call-time validation
	validateFileInputs   bool              // WithFileInputValidation enables SEP-2356 size + MIME enforcement
	publicMethods        map[string]bool   // methods that bypass auth (pre-auth discovery)
	customHandlers       map[string]MethodHandler // custom JSON-RPC method handlers
	httpHandlers         []httpHandlerEntry       // custom HTTP endpoint handlers
	requestStateKey      []byte                   // SEP-2322 requestState HMAC key — shared by MRTR + Tasks (nil = plaintext / unsigned)
	requestStateTTL      time.Duration            // SEP-2322 requestState validity (0 = 24h default)
	listTTLMs            *int                     // SEP-2549 cache-freshness hint (ms) attached to every list response (nil = omit)
	listCacheScope       string                   // SEP-2549 cacheScope attached to every list response ("" = omit)
	readTTLMs            *int                     // SEP-2549 resources/read default cache-freshness hint (ms); handler may override per-read
	readCacheScope       string                   // SEP-2549 resources/read default cacheScope; handler may override per-read
	allowLegacyOnDraft   bool                     // WithAllowLegacyOnDraft — opt-in SEP-2575 leniency on the legacy wire (off by default; strict per spec)
	allowReinitialize    bool                     // WithAllowReinitialize — opt-in acceptance of a duplicate initialize (off by default; issue 421)
	taskBucketKeyer      core.TaskBucketKeyer     // WithTaskBucketKeyer — per-request task-store isolation bucket (nil = session ID; issue 485)
	supportedVersions    []string                 // WithSupportedVersions — per-server protocol version override (nil = package default; issue 419)
	tracerProvider       core.TracerProvider      // SEP-414 P2 — WithTracerProvider; nil/Noop = trace middleware not installed
	meterProvider        core.MeterProvider       // issue 7 — WithMeterProvider; nil/Noop = metrics middleware not installed
	notificationRelay       NotificationRelay           // issue 755 — WithNotificationRelay; nil = no cross-replica broadcast (Broadcast fires local only)
}

type httpHandlerEntry struct {
	pattern string
	handler http.Handler
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

// WithPublicMethods declares JSON-RPC methods that bypass auth and are
// accessible without a valid token. Use this for pre-auth capability
// discovery — clients can call these methods to learn what the server
// offers before deciding to authenticate.
//
// Default: none (all methods require auth when WithAuth is configured).
//
// Recommended set for discovery:
//
//	server.WithPublicMethods("initialize", "notifications/initialized",
//	    "tools/list", "resources/list", "resources/templates/list",
//	    "prompts/list", "ping")
func WithPublicMethods(methods ...string) Option {
	return func(o *serverOptions) {
		if o.publicMethods == nil {
			o.publicMethods = make(map[string]bool)
		}
		for _, m := range methods {
			o.publicMethods[m] = true
		}
	}
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
//
// For public-facing deployments also pass [WithSubscriptionCap] (and
// optionally [WithSubscriptionRateLimit]); without those a misbehaving
// client can subscribe unboundedly and exhaust server resources.
func WithSubscriptions() Option {
	return func(o *serverOptions) { o.subscriptionsEnabled = true }
}

// SubscriptionRejectFunc is invoked when resources/subscribe is refused
// because the session has hit the configured cap or rate limit. reason is
// one of "cap_exceeded" or "rate_limited". The hook is called outside the
// subscription registry's lock; implementations may block but doing so
// will not delay other requests on the session.
type SubscriptionRejectFunc func(sessionID, uri, reason string)

// WithSubscriptionCap sets the maximum number of concurrent
// resources/subscribe registrations a single session may hold. Once the
// session is at the cap, additional resources/subscribe calls fail with
// [core.ErrCodeSubscriptionLimitExceeded] (-32010). Unsubscribing frees
// a slot for the same session.
//
// Off by default (n <= 0 means unlimited). Recommended for any
// public-facing deployment; 100 is a reasonable starting point. The cap
// is per-session, not global — two sessions can each hold n
// subscriptions simultaneously.
func WithSubscriptionCap(n int) Option {
	return func(o *serverOptions) { o.subscriptionCap = n }
}

// WithSubscriptionRateLimit bounds how fast a single session may issue
// resources/subscribe calls. Implemented as a per-session token bucket
// with refill rate r and burst b. burst is the largest spike the bucket
// will admit before throttling kicks in; the steady-state ceiling is r
// subscribes per second per session. Exceeded calls fail with
// [core.ErrCodeSubscriptionLimitExceeded] (-32010) carrying reason
// "rate_limited".
//
// Off by default (r <= 0 means unlimited). unsubscribe is not metered —
// the goal is to bound the cost of registration churn, not to penalize
// cleanup.
func WithSubscriptionRateLimit(r rate.Limit, burst int) Option {
	return func(o *serverOptions) {
		o.subscriptionRate = r
		o.subscriptionBurst = burst
	}
}

// WithSubscriptionRejectHook installs a callback fired every time a
// resources/subscribe is refused by [WithSubscriptionCap] or
// [WithSubscriptionRateLimit], and every time a SEP-2575
// subscriptions/listen stream is refused by
// [WithStatelessSubscriptionCap] or [WithStatelessSubscriptionRateLimit].
// One callback covers both wires so operators only need to wire
// observability once.
//
// Argument semantics:
//
//   - On the legacy wire: arg1 is the sessionID, arg2 is the resource
//     URI the client tried to subscribe to.
//   - On the stateless wire: arg1 is the scope key returned by the
//     configured [StatelessSubscriptionScopeFunc] (host of RemoteAddr
//     by default), arg2 is the literal string "subscriptions/listen".
//
// reason is "cap_exceeded" or "rate_limited" on both wires.
func WithSubscriptionRejectHook(fn SubscriptionRejectFunc) Option {
	return func(o *serverOptions) { o.subscriptionReject = fn }
}

// DefaultStatelessSubscriptionCap is the default per-scope concurrent
// subscriptions/listen stream cap applied when the caller does not
// override it via [WithStatelessSubscriptionCap]. Chosen to give
// public-facing deployments out-of-the-box defense against churn — same
// posture as [net/http.Transport.MaxIdleConns]. Pass a negative number
// to [WithStatelessSubscriptionCap] to disable the cap entirely.
const DefaultStatelessSubscriptionCap = 100

// effectiveStatelessSubCap maps the raw option value stored in
// serverOptions to the cap newStatelessSubMap should use. The rule
// keeps the wire-level "0 or negative = unlimited" semantic on the
// internal registry intact and applies the public default at the
// option boundary:
//
//   - 0  (option never set): apply [DefaultStatelessSubscriptionCap].
//   - <0 (explicit opt-out): disable the cap (pass 0 to the registry,
//     which treats 0 as unlimited).
//   - >0 (explicit value):   pass through.
func effectiveStatelessSubCap(raw int) int {
	switch {
	case raw == 0:
		return DefaultStatelessSubscriptionCap
	case raw < 0:
		return 0
	default:
		return raw
	}
}

// WithStatelessSubscriptionCap sets the maximum number of concurrent
// SEP-2575 subscriptions/listen streams the transport will accept from
// a single scope (typically a remote host, configurable via
// [WithStatelessSubscriptionScope]). Once the scope is at the cap,
// additional subscriptions/listen requests fail with
// [core.ErrCodeSubscriptionLimitExceeded] (-32010) before the SSE
// stream is opened. Closing a stream frees a slot for the same scope.
//
// Defaults to [DefaultStatelessSubscriptionCap] (100) when the option
// is not called — public-facing deployments are protected out of the
// box. Pass n > 0 to override, or n < 0 to disable the cap entirely.
// (n == 0 is treated as "use default" so that the zero value of
// serverOptions picks up the default.)
//
// Stability: the wire surface this guards (SEP-2575
// subscriptions/listen) is in the 2026-07-28 release candidate, not
// Final. The cap is mcpkit-internal and not spec-defined; if a future
// SEP standardizes subscription caps, callers may need to migrate.
// See docs/SEP_2640_STATELESS_INTERACTION.md.
func WithStatelessSubscriptionCap(n int) Option {
	return func(o *serverOptions) { o.statelessSubCap = n }
}

// WithStatelessSubscriptionRateLimit bounds how fast a single scope may
// open new subscriptions/listen streams. Implemented as a per-scope
// token bucket with refill rate r and burst b. Exceeded calls fail
// with [core.ErrCodeSubscriptionLimitExceeded] (-32010) carrying
// reason "rate_limited" in error.data.
//
// Off by default (r <= 0 means unlimited). Disconnects are not metered;
// the cost being bounded is registration churn, not normal cleanup.
//
// Stability: see [WithStatelessSubscriptionCap].
func WithStatelessSubscriptionRateLimit(r rate.Limit, burst int) Option {
	return func(o *serverOptions) {
		o.statelessSubRate = r
		o.statelessSubBurst = burst
	}
}

// WithStatelessSubscriptionScope overrides the scope-key extractor used
// by [WithStatelessSubscriptionCap] and
// [WithStatelessSubscriptionRateLimit]. The default
// ([DefaultStatelessSubscriptionScope]) returns the host portion of
// r.RemoteAddr, which is correct for deployments where every client
// has a distinct OS-visible source IP, but lumps all clients behind a
// shared reverse proxy into one bucket.
//
// Override only when the operator vouches for the proxy chain: an
// X-Forwarded-For-aware extractor lets a malicious client spoof a scope
// key. Document the trust assumption in the deployment.
func WithStatelessSubscriptionScope(fn StatelessSubscriptionScopeFunc) Option {
	return func(o *serverOptions) { o.statelessSubScope = fn }
}

// WithToolTimeout sets the maximum duration for tool execution.
func WithToolTimeout(d time.Duration) Option {
	return func(o *serverOptions) { o.toolTimeout = d }
}

// WithListTTLMs configures the SEP-2549 cache-freshness hint, in integer
// milliseconds, attached to every tools/list, prompts/list, resources/list,
// and resources/templates/list response. The hint tells clients how long
// they MAY serve a cached copy of the list before re-fetching:
//
//   - Negative values are treated as "no hint" (the wire field is omitted),
//     so clients fall back to notifications/list_changed or their own
//     heuristics. This is also the default when the option is not set.
//   - 0 sends an explicit `"ttlMs": 0` — the response SHOULD be considered
//     immediately stale; clients MAY re-fetch every time the list is needed.
//   - Positive values send `"ttlMs": N` meaning "fresh for N milliseconds";
//     clients SHOULD NOT re-fetch before it expires unless they receive a
//     list_changed notification.
//
// The hint applies uniformly to all four list endpoints. To also set the
// SEP-2549 cacheScope in one call, use WithListCacheControl.
//
// SEP-2549's final review renamed this option's predecessor WithListTTL
// (integer seconds) to milliseconds. See docs/LIST_TTL_MIGRATION.md.
func WithListTTLMs(ms int) Option {
	return func(o *serverOptions) {
		if ms < 0 {
			o.listTTLMs = nil
			return
		}
		// Allocate a new int so multiple servers with different TTLs
		// don't share storage.
		v := ms
		o.listTTLMs = &v
	}
}

// WithListCacheControl configures both SEP-2549 list cache hints in a
// single call: ttlMs (see WithListTTLMs for value semantics) and cacheScope
// (core.CacheScopePublic or core.CacheScopePrivate; "" omits the field,
// which clients default to "public"). The hints apply uniformly to all four
// list endpoints. Use WithListTTLMs when only the TTL is needed.
func WithListCacheControl(ttlMs int, scope string) Option {
	return func(o *serverOptions) {
		if ttlMs < 0 {
			o.listTTLMs = nil
		} else {
			v := ttlMs
			o.listTTLMs = &v
		}
		o.listCacheScope = scope
	}
}

// WithReadResourceCacheControl sets the default SEP-2549 cache hints for
// resources/read responses: ttlMs (integer milliseconds; negative omits the
// field) and cacheScope (core.CacheScopePublic or CacheScopePrivate).
//
// A resource (or template) handler MAY override either hint per-read by
// setting core.ResourceResult.TTLMs / .CacheScope on its return value; the
// server applies these defaults only to fields the handler left unset.
//
// resources/read responses frequently depend on the authenticated user —
// pass core.CacheScopePrivate unless the content is identical for every
// caller. See docs/LIST_TTL_MIGRATION.md for the security rationale.
func WithReadResourceCacheControl(ttlMs int, scope string) Option {
	return func(o *serverOptions) {
		if ttlMs < 0 {
			o.readTTLMs = nil
		} else {
			v := ttlMs
			o.readTTLMs = &v
		}
		o.readCacheScope = scope
	}
}

// WithoutListCacheControl turns OFF the conformant-by-default SEP-2549 list
// cache hints (issue 496), so tools/list, prompts/list, resources/list, and
// resources/templates/list responses omit both ttlMs and cacheScope. Use this
// only if you deliberately want no cache-control hint on list responses; most
// servers should keep the default (ttlMs:0, scope:public) or set explicit
// values with WithListCacheControl.
func WithoutListCacheControl() Option {
	return func(o *serverOptions) {
		o.listTTLMs = nil
		o.listCacheScope = ""
	}
}

// WithoutReadResourceCacheControl turns OFF the conformant-by-default SEP-2549
// resources/read cache hints (issue 496), so read responses omit both ttlMs and
// cacheScope unless a handler sets them per-read. Prefer keeping the default
// (ttlMs:0, scope:private) or setting explicit values with
// WithReadResourceCacheControl.
func WithoutReadResourceCacheControl() Option {
	return func(o *serverOptions) {
		o.readTTLMs = nil
		o.readCacheScope = ""
	}
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

// WithFileInputValidation enables SEP-2356 server-side validation of
// file-typed tool arguments. When enabled, the dispatcher walks each
// tool's inputSchema for `x-mcp-file` properties (single string/uri or
// array-items shape) and runs `core.ValidateFileInput` on every matching
// argument BEFORE the handler is invoked.
//
// Failures surface as JSON-RPC -32602 with structured `data`:
//
//	{
//	  "code": -32602,
//	  "message": "file input \"image\" exceeds size limit",
//	  "data": {
//	    "reason": "file_too_large",
//	    "field": "image",
//	    "actualSize": 5243904,
//	    "maxSize": 5242880
//	  }
//	}
//
// The wire shape is frozen by panyam/mcpconformance `pending` (`src/scenarios/server/file-inputs/`)
// (the cross-impl contract). Disabled by default — handlers that prefer
// to validate themselves can leave it off.
//
// Usage:
//
//	srv := server.NewServer(info,
//	    server.WithFileInputValidation(),
//	)
func WithFileInputValidation() Option {
	return func(o *serverOptions) { o.validateFileInputs = true }
}

// WithAllowedRoots restricts tool cwd to the given directory prefixes.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func WithAllowedRoots(roots ...string) Option {
	return func(o *serverOptions) { o.allowedRoots = roots }
}

// WithAllowLegacyOnDraft is an opt-in back-compat escape hatch for the
// SEP-2575 enforcement on 2026-07-28. When set, the legacy session
// wire (initialize + Mcp-Session-Id) is accepted on the draft protocol
// version without enforcing the per-request _meta envelope on follow-up
// requests.
//
// Default (option NOT set): the dispatcher enforces SEP-2575 strictly —
// on 2026-07-28, every post-initialize request MUST carry
// `params._meta.io.modelcontextprotocol/{protocolVersion, clientInfo,
// clientCapabilities}`; missing _meta is rejected with -32602.
//
// Use this only if you have legacy clients pinned to 2026-07-28 that
// haven't migrated to per-request metadata yet. New servers should leave
// this off so non-conformant clients fail loudly.
func WithAllowLegacyOnDraft() Option {
	return func(o *serverOptions) { o.allowLegacyOnDraft = true }
}

// WithAllowReinitialize opts into accepting a second initialize on an
// already-negotiated session (protocol re-negotiation).
//
// Default (option NOT set): once a session has negotiated a protocol version,
// a duplicate initialize is rejected with -32600 Invalid Request and the
// existing session state (negotiated version, client capabilities, client
// identity) is preserved. This stops a misbehaving or hostile client from
// rewriting session state mid-flight — downgrading the negotiated version or
// changing the advertised client identity (issue 421).
//
// Set this only if your deployment genuinely re-negotiates the protocol on a
// live session.
func WithAllowReinitialize() Option {
	return func(o *serverOptions) { o.allowReinitialize = true }
}

// WithTaskBucketKeyer sets how the task store isolates tasks per request
// (issue 485). The keyer maps a request context to the bucket key used as the
// store's sessionID argument for Create / Get / Update / Cancel / List.
//
// Default (option NOT set): the bucket is the transport session ID. On the
// legacy wire that is the per-connection session; on the SEP-2575 stateless
// wire it is "" (no session), so all stateless tasks share one bucket per
// process — fine for single-tenant deployments, a cross-tenant isolation hole
// for multi-tenant ones.
//
// Multi-tenant stateless deployments install a keyer that derives the bucket
// from an authenticated subject so tenants cannot see each other's tasks, e.g.:
//
//	server.WithTaskBucketKeyer(func(ctx context.Context) string {
//	    return auth.SubjectFromContext(ctx) // your auth accessor
//	})
//
// The keyer takes a raw context.Context, so mcpkit never imports ext/auth — the
// coupling to auth is code the deployer writes. Applies to both the v1
// (RegisterTasksV1) and v2 (ext/tasks) surfaces and both wires.
func WithTaskBucketKeyer(keyer core.TaskBucketKeyer) Option {
	return func(o *serverOptions) { o.taskBucketKeyer = keyer }
}

// WithSupportedVersions overrides, for this server only, the set of MCP
// protocol versions accepted at initialize and on the MCP-Protocol-Version
// header (issue 419). Pass the versions the server should support, newest
// first — the first entry is the one the server offers when a client requests
// a version outside the set.
//
// Default (option NOT set): the package-level default
// (2026-07-28 / 2025-11-25 / 2025-03-26 / 2024-11-05). Operators use this to
// drop older versions per deployment, e.g. refuse 2024-11-05:
//
//	server.WithSupportedVersions("2026-07-28", "2025-11-25", "2025-03-26")
//
// Behavior with the configured set:
//   - initialize requesting a version in the set negotiates that version;
//     requesting one outside the set negotiates the set's preferred (first)
//     version, and the client proceeds on it or disconnects (MCP 2025-03-26
//     §Version Negotiation — same as the default handshake, just over a
//     narrower set).
//   - a post-initialize MCP-Protocol-Version header carrying a version outside
//     the set is rejected with HTTP 400.
//
// Passing an empty list is a no-op (the default set stays in effect); order is
// preserved as given.
func WithSupportedVersions(versions ...string) Option {
	return func(o *serverOptions) {
		if len(versions) > 0 {
			o.supportedVersions = versions
		}
	}
}

// WithRootsFetchTimeout sets the deadline for server-to-client roots/list
// requests issued after notifications/roots/list_changed. Default is 30s.
// Decrease for aggressive fail-fast; increase for slow clients with large
// root enumerations (monorepos, network mounts). Issue #198.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func WithRootsFetchTimeout(d time.Duration) Option {
	return func(o *serverOptions) { o.rootsFetchTimeout = d }
}

// NewServer creates an MCP server with the given identity and options.
func NewServer(info core.ServerInfo, opts ...Option) *Server {
	s := &Server{
		dispatcher: NewDispatcher(info),
	}
	// Conformant-by-default (issue 496): seed the SEP-2549 cache-control hints
	// that have a safe, semantically-equivalent zero value BEFORE applying user
	// options, so servers are spec-conformant on the SEP-2549 MUST parts without
	// the caller having to know the option exists. ttlMs:0 = "immediately stale"
	// (same effective behavior as omitting the field, but present so the
	// conformance check passes); list scope defaults public, read scope defaults
	// private (conservative — resources/read often varies per authenticated
	// user). Callers override with WithList/ReadResourceCacheControl or turn the
	// hints off entirely with WithoutList/ReadResourceCacheControl.
	listTTLDefault := 0
	readTTLDefault := 0
	s.options.listTTLMs = &listTTLDefault
	s.options.listCacheScope = core.CacheScopePublic
	s.options.readTTLMs = &readTTLDefault
	s.options.readCacheScope = core.CacheScopePrivate

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
	s.dispatcher.validateFileInputs = s.options.validateFileInputs
	s.dispatcher.allowLegacyOnDraft = s.options.allowLegacyOnDraft
	s.dispatcher.allowReinitialize = s.options.allowReinitialize
	s.dispatcher.taskBucketKeyer = s.options.taskBucketKeyer
	s.dispatcher.configuredVersions = s.options.supportedVersions
	s.dispatcher.customHandlers = s.options.customHandlers
	// Wire registry change notifications to Server.Broadcast so that
	// dynamic adds/removes automatically notify all connected sessions.
	s.dispatcher.Reg.OnChange = func(method string) {
		s.Broadcast(context.Background(), method, nil)
	}
	// Wire roots configuration
	if s.options.onRootsChanged != nil {
		s.dispatcher.onRootsChanged = s.options.onRootsChanged
	}
	s.dispatcher.rootsFetchTimeout = s.options.rootsFetchTimeout
	s.dispatcher.allowedRoots = s.options.allowedRoots
	// SEP-2322 requestState signing — shared by ephemeral MRTR (this Dispatcher's
	// mrtr runtime) and SEP-2663 Tasks (consumed by RegisterTasks via the
	// server's options when TasksConfig.RequestStateKey is unset).
	if len(s.options.requestStateKey) > 0 {
		s.dispatcher.mrtr.signingKey = s.options.requestStateKey
	}
	s.dispatcher.mrtr.ttl = s.options.requestStateTTL
	// SEP-2549 cache hints — propagated unchanged (nil/"" = field omitted).
	s.dispatcher.listTTLMs = s.options.listTTLMs
	s.dispatcher.listCacheScope = s.options.listCacheScope
	s.dispatcher.readTTLMs = s.options.readTTLMs
	s.dispatcher.readCacheScope = s.options.readCacheScope
	// issue 7 — install metrics middleware (and the sessions-active
	// up-down counter for the streamable transport) when a non-Noop
	// MeterProvider is configured. Zero-overhead when nil / Noop —
	// the dispatch loop's `if s.metricsMiddleware != nil` branch
	// short-circuits before any instrument lookup.
	if metricsEnabled(s.options.meterProvider) {
		s.metricsMiddleware = newMetricsMiddleware(s.options.meterProvider)
		s.sessionsActive = newSessionsActiveCounter(s.options.meterProvider)
	}
	// Initialize subscription support if enabled
	if s.options.subscriptionsEnabled {
		s.subRegistry = &subscriptionRegistry{
			subscribers:    make(map[string]map[string]*Dispatcher),
			counts:         make(map[string]int),
			limiters:       make(map[string]*rate.Limiter),
			cap:            s.options.subscriptionCap,
			rateLimit:      s.options.subscriptionRate,
			rateBurst:      s.options.subscriptionBurst,
			onReject:       s.options.subscriptionReject,
			notificationRelay: s.options.notificationRelay,
		}
		s.dispatcher.subscriptionsEnabled = true
		s.dispatcher.subManager = s.subRegistry
	}
	return s
}

// HandleMethod registers a handler for a custom JSON-RPC method.
// Panics if the method is a built-in MCP spec method.
func (s *Server) HandleMethod(method string, h MethodHandler) {
	if builtinMethods[method] {
		panic("mcpkit: cannot override built-in MCP method: " + method)
	}
	if s.dispatcher.customHandlers == nil {
		s.dispatcher.customHandlers = make(map[string]MethodHandler)
	}
	s.dispatcher.customHandlers[method] = h
}

// UseMiddleware appends server-side middleware post-construction. Must be
// called before accepting connections (same constraint as HandleMethod).
// For construction-time registration, prefer WithMiddleware().
func (s *Server) UseMiddleware(mw ...Middleware) {
	s.options.middleware = append(s.options.middleware, mw...)
}

// SetTasksCap configures the tasks capability advertised during initialize.
// Must be called before accepting connections. (V1-only: v2 advertises tasks
// via the io.modelcontextprotocol/tasks extension instead — see RegisterTasks.)
func (s *Server) SetTasksCap(cap *core.TasksCap) {
	s.dispatcher.tasksCap = cap
}

// RegisterExtension declares a protocol extension at runtime, the way
// WithExtension does at construction time. Used by RegisterTasks (and other
// post-construction hookups like ext/auth) so the extension is advertised in
// the initialize response without forcing callers to thread an Option through.
// Must be called before accepting connections.
func (s *Server) RegisterExtension(ext core.ExtensionProvider) {
	if s.dispatcher.extensions == nil {
		s.dispatcher.extensions = make(map[string]core.Extension)
	}
	e := ext.Extension()
	s.dispatcher.extensions[e.ID] = e
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
//
// Returns the JSON-RPC response and an optional transport-level error.
// A non-nil error (typically *core.AuthError) indicates middleware short-circuited
// at the transport layer (e.g., scope step-up); callers should map it to an
// HTTP-level response via writeAuthError.
func (s *Server) Dispatch(ctx context.Context, req *core.Request) (*core.Response, error) {
	return s.dispatchWith(s.dispatcher, ctx, nil, req)
}

// dispatchWith routes a request through a specific dispatcher with server-level
// middleware (e.g. tool timeout). Used by transports to dispatch on per-session
// dispatchers. The claims parameter carries the authenticated identity from CheckAuth.
func (s *Server) dispatchWith(d *Dispatcher, ctx context.Context, claims *core.Claims, req *core.Request) (*core.Response, error) {
	return s.dispatchWithNotify(d, ctx, claims, d.getNotifyFunc(), req)
}

// dispatchWithNotify is like dispatchWith but accepts an explicit core.NotifyFunc.
// Used by handlePostSSE to pass a request-scoped notify function that writes
// to the current SSE stream, avoiding races on d.notifyFunc when concurrent
// SSE-streaming POSTs share the same session dispatcher.
func (s *Server) dispatchWithNotify(d *Dispatcher, ctx context.Context, claims *core.Claims, notify core.NotifyFunc, req *core.Request) (*core.Response, error) {
	return s.dispatchWithNotifyAndRequest(d, ctx, claims, notify, nil, req)
}

// dispatchWithNotifyAndRequest is the full dispatch entry point that accepts both
// a core.NotifyFunc and core.RequestFunc. Used by transports that support server-to-client requests.
func (s *Server) dispatchWithNotifyAndRequest(d *Dispatcher, ctx context.Context, claims *core.Claims, notify core.NotifyFunc, request core.RequestFunc, req *core.Request) (*core.Response, error) {
	return s.dispatchWithOpts(d, ctx, claims, notify, request, nil, req)
}

// dispatchWithOpts is the full dispatch entry point including the optional
// SSE retry-hint emitter (#72). SSE-capable transports (sseTransport, the
// streamable HTTP SSE POST path) pass a non-nil sseRetry so handlers calling
// core.EmitSSERetry can emit a "retry:" field on the current stream.
// Non-SSE transports pass nil and EmitSSERetry becomes a silent no-op.
func (s *Server) dispatchWithOpts(d *Dispatcher, ctx context.Context, claims *core.Claims, notify core.NotifyFunc, request core.RequestFunc, sseRetry func(ms int), req *core.Request) (*core.Response, error) {
	// Wrap outgoing notifications with interceptors (first registered = outermost).
	for i := len(s.options.notifyInterceptors) - 1; i >= 0; i-- {
		next := notify
		interceptor := s.options.notifyInterceptors[i]
		notify = func(method string, params any) { interceptor(method, params, next) }
	}

	// Wrap outgoing server-to-client requests with interceptors.
	if request != nil {
		for i := len(s.options.requestInterceptors) - 1; i >= 0; i-- {
			next := request
			interceptor := s.options.requestInterceptors[i]
			request = func(ctx context.Context, method string, params any) (json.RawMessage, error) {
				return interceptor(ctx, method, params, next)
			}
		}
	}

	// Inject session context so tool handlers can send notifications, requests,
	// and access authenticated claims and client capabilities.
	ctx = core.ContextWithSession(ctx, notify, request, &d.logLevel, &d.clientCaps, claims)

	// Set the session ID so middleware and handlers can access it via ctx.SessionID().
	core.SetSessionID(ctx, d.sessionID)

	// Install the task-store bucket keyer (issue 485) so the v1/v2 task
	// surfaces resolve isolation via core.TaskBucketKey. Nil = no-op (default
	// session-ID keying).
	ctx = core.WithTaskBucketKeyer(ctx, d.taskBucketKeyer)

	// SEP-2243: stage the Mcp-Method response header carrying the JSON-RPC
	// method name (e.g. "tools/call", "tasks/get"). Pairs with Mcp-Name
	// (taskId, set by the v2 task middleware on task-creating tools/call) so
	// HTTP infrastructure can route / log against the request shape without
	// parsing the JSON body. Notifications go nowhere (dispatch returns nil),
	// so the header is harmlessly dropped for those — only RPC responses
	// carry it through to the wire.
	if req.Method != "" {
		core.SetResponseHeader(ctx, "Mcp-Method", req.Method)
	}

	// Register a detach strategy for background goroutines (e.g., async tasks).
	// The strategy replaces the POST-scoped requestFunc and notifyFunc (which
	// write to the now-closed response stream) with session-level equivalents
	// that use the persistent GET SSE stream. This allows background goroutines
	// to send notifications (progress, logging) and server-to-client requests
	// (elicitation, sampling) after the original HTTP request has returned.
	ctx = core.SetDetachStrategy(ctx, func(c context.Context) context.Context {
		c = context.WithoutCancel(c)
		// Replace transport-scoped functions. Each Replace* returns a new
		// context — chain them so both replacements take effect.
		if push := d.getPushRequest(); push != nil {
			c = core.ReplaceSessionRequestFunc(c, d.makeRequestFunc(push))
		}
		if bgNotify := d.getNotifyFunc(); bgNotify != nil {
			c = core.ReplaceSessionNotifyFunc(c, bgNotify)
		}
		return c
	})

	// Wire the SSE retry-hint emitter if provided by the transport layer.
	if sseRetry != nil {
		ctx = core.SetSSERetryHint(ctx, sseRetry)
	}

	// Wire allowed-roots enforcement (#197). The closure computes the
	// effective roots on each call: intersection of static WithAllowedRoots
	// and dynamic client roots. If no static list is set, client roots are
	// the full policy. If no client roots fetched yet, static list only.
	if len(d.allowedRoots) > 0 || d.onRootsChanged != nil {
		ctx = core.SetAllowedRoots(ctx, func() []string {
			return d.effectiveAllowedRoots()
		})
	}

	// Wire handler-accessible NotifyResourceUpdated (#208). Routes through
	// the subscription registry to fan out to all subscribed sessions.
	if s.subRegistry != nil {
		ctx = core.SetNotifyResourceUpdated(ctx, func(uri string) {
			s.subRegistry.notify(uri)
		})
	}

	// Inject custom content chunk method if configured.
	if s.options.contentChunkMethod != "" {
		ctx = core.WithContentChunkMethod(ctx, s.options.contentChunkMethod)
	}

	// Build the terminal handler: dispatch with optional tool timeout.
	handler := MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		if s.options.toolTimeout > 0 && req.Method == "tools/call" {
			tctx, cancel := context.WithTimeout(ctx, s.options.toolTimeout)
			defer cancel()
			return d.Dispatch(tctx, req), nil
		}
		return d.Dispatch(ctx, req), nil
	})

	// Wrap with user middleware (reverse order: first registered = outermost).
	for i := len(s.options.middleware) - 1; i >= 0; i-- {
		next := handler
		mw := s.options.middleware[i]
		handler = func(ctx context.Context, req *core.Request) (*core.Response, error) {
			return mw(ctx, req, next)
		}
	}

	// SEP-414 P2 — wrap with the trace middleware as the outermost layer so
	// user middleware (rate limit, audit, custom auth) executes inside the
	// span and contributes to the recorded latency. Skipped when no
	// TracerProvider is configured or the default NoopTracerProvider is in
	// use — zero overhead on the default path.
	//
	// The metrics middleware (issue 7) sits one layer INSIDE the trace
	// middleware so its mcp.tool.duration histogram observation excludes
	// the trace span-creation overhead and matches the latency a caller
	// would actually attribute to the handler. The trace span still wraps
	// the whole pipeline so metrics-induced overhead is visible in Tempo.
	if s.metricsMiddleware != nil {
		next := handler
		mw := s.metricsMiddleware
		handler = func(ctx context.Context, req *core.Request) (*core.Response, error) {
			return mw(ctx, req, next)
		}
	}
	if tracingEnabled(s.options.tracerProvider) {
		next := handler
		mw := traceMiddleware(s.options.tracerProvider)
		handler = func(ctx context.Context, req *core.Request) (*core.Response, error) {
			return mw(ctx, req, next)
		}
	}

	return handler(ctx, req)
}

// tracingEnabled reports whether the configured TracerProvider should
// install the SEP-414 trace middleware. nil and the default
// core.NoopTracerProvider both report false so the zero-overhead path is
// preserved when no tracing is wired.
func tracingEnabled(tp core.TracerProvider) bool {
	if tp == nil {
		return false
	}
	if _, isNoop := tp.(core.NoopTracerProvider); isNoop {
		return false
	}
	return true
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
// ctx threads through SEP-414 trace context. When ctx carries a non-zero
// core.TraceContext, the inbound traceparent / tracestate are injected
// into the notification params under `_meta` before fan-out, so SSE
// subscribers see the same trace identity the originating yield ran
// under. Existing caller-set `_meta.traceparent` on params wins
// (delegates to core.InjectTraceContextIntoParams's contract).
//
// The injection happens once at the Server level rather than per
// transport because the per-session notifyFunc the transport
// dispatchers expose is the BASE notifyFunc — the per-request
// trace_middleware wrap (`core.WrapSessionNotifyFunc`) targets
// `sc.notify` on a per-request SessionCtx, which broadcast doesn't
// run inside. Injecting once at the call site keeps tracing concerns
// out of the transport-level broadcast loops.
//
// Example:
//
//	// After registering a new tool at runtime:
//	srv.Broadcast(ctx, "notifications/tools/list_changed", nil)
func (s *Server) Broadcast(ctx context.Context, method string, params any) {
	if tc := core.TraceContextFromContext(ctx); !tc.IsZero() {
		params = core.InjectTraceContextIntoParams(params, tc)
	}
	s.mu.Lock()
	relay := s.options.notificationRelay
	s.mu.Unlock()
	if relay != nil {
		relay.Publish(ctx, method, params)
	}
	s.BroadcastToSessions(ctx, method, params)
}

// BroadcastToSessions delivers a notification to every locally-connected
// client via the per-transport session broadcasters. UNLIKE Broadcast,
// BroadcastToSessions does NOT invoke the installed NotificationRelay —
// it's the local-only path Pattern B receivers call after dropping
// self-publishes, so they don't re-publish through the relay and loop.
//
// External callers should normally use Broadcast (which fires the
// relay + local). BroadcastToSessions is the entry point for relay
// receive callbacks (server.CapabilityBroadcastReceiver and equivalent
// adapters) and tests that want to verify session fan-out independently
// of the relay path.
//
// Trace context injection: BroadcastToSessions does NOT re-inject
// _meta.traceparent — the relay receive path is responsible for
// surfacing the upstream trace context onto ctx, and the params
// envelope already carries any caller-set _meta. Re-injecting here
// would double-stamp on the local-fanout-only path called from
// Broadcast above.
func (s *Server) BroadcastToSessions(ctx context.Context, method string, params any) {
	s.mu.Lock()
	broadcasters := make([]func(context.Context, string, any), len(s.sessionBroadcasters))
	copy(broadcasters, s.sessionBroadcasters)
	s.mu.Unlock()

	for _, bc := range broadcasters {
		bc(ctx, method, params)
	}
}

// registerTransportSessions registers a transport's session management callbacks.
// Called by transports during Handler() creation.
func (s *Server) registerTransportSessions(closeOne sessionCloser, closeAll func(), broadcast func(ctx context.Context, method string, params any)) {
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

	// Mount custom HTTP handlers if configured.
	if len(s.options.httpHandlers) > 0 {
		mux := http.NewServeMux()
		mux.Handle("/", handler) // transport handler as default
		for _, entry := range s.options.httpHandlers {
			mux.Handle(entry.pattern, entry.handler)
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

	mcpHandler := s.Handler(opts...)

	// Close all sessions on shutdown so SSE/Streamable HTTP handlers unblock
	// and srv.Shutdown() can drain immediately.
	shutdownFns := []func(){s.CloseAllSessions}

	// If muxSetup is provided, wrap the MCP handler in a mux with additional routes.
	var handler http.Handler = mcpHandler
	if cfg.muxSetup != nil {
		mux := http.NewServeMux()
		mux.Handle(cfg.prefix, mcpHandler)
		cfg.muxSetup(mux)
		handler = mux
	}
	// Apply caller-supplied handler wraps (CORS, rate limiting, etc.).
	// First-registered is innermost; last-registered is outermost.
	for _, wrap := range cfg.handlerWraps {
		handler = wrap(handler)
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
	stateless bool // stateless mode: no sessions, fresh dispatcher per request
	// statelessMode picks the wire — SEP-2575 stateless, legacy session,
	// or both on one URL. Orthogonal to stateless above; the latter is
	// process-architecture, this is the protocol shape. See stateless.Mode.
	// Seeded by defaultTransportConfig from MCPKIT_STATELESS_MODE env or
	// stateless.DefaultMode; WithStatelessMode option overrides if passed.
	statelessMode  stateless.Mode
	sessionTimeout time.Duration // idle timeout for Streamable HTTP sessions (0 = no timeout)
	eventStore     gohttp.EventStore // optional: persists SSE events for Last-Event-ID replay
	keepaliveInterval time.Duration  // 0 = disabled; interval for JSON-RPC ping requests
	keepaliveMaxFails int            // max consecutive ping failures before session cleanup (default 3)
	sseGracePeriod    time.Duration  // 0 = immediate cleanup on SSE disconnect (backward compat)
	muxSetup          func(*http.ServeMux) // optional: register additional routes on the server mux
	handlerWraps      []func(http.Handler) http.Handler // applied after mux composition; first registered = innermost
}

func defaultTransportConfig() transportConfig {
	return transportConfig{
		prefix:          "/mcp",
		keepalivePeriod: 30 * time.Second,
		sse:             true,
		streamableHTTP:  false,
		statelessMode:   stateless.ResolveMode(),
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

// WithMux registers additional HTTP routes on the server's mux alongside
// the MCP transport handler. Use this to mount auth discovery (PRM), health
// checks, or other endpoints without dropping out of srv.Run()'s graceful
// shutdown.
//
// Example:
//
//	srv.Run(":8080",
//	    server.WithStreamableHTTP(true),
//	    server.WithMux(func(mux *http.ServeMux) {
//	        auth.MountAuth(mux, authCfg)
//	        mux.HandleFunc("/healthz", healthHandler)
//	    }),
//	)
func WithMux(setup func(*http.ServeMux)) TransportOption {
	return func(c *transportConfig) { c.muxSetup = setup }
}

// WithHandlerWrap wraps the final HTTP handler (after WithMux composition,
// if any) before it's served. Useful for cross-cutting concerns that should
// apply to every route the server exposes — CORS for browser-based MCP
// hosts, rate limiting, request tracing, etc.
//
// Multiple WithHandlerWrap options compose: the first one registered is
// innermost (closest to the MCP handler), and the last one registered is
// outermost (the first to see an incoming request and last to see the
// outgoing response). This matches conventional HTTP middleware ordering.
//
// Example — applying CORS so MCPJam (browser-based) can connect:
//
//	cors := middleware.CORS(nil,
//	    middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
//	    middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id"),
//	    middleware.CORSExposeHeaders("Mcp-Session-Id"),
//	)
//	srv.ListenAndServe(
//	    server.WithStreamableHTTP(true),
//	    server.WithMux(func(m *http.ServeMux) { m.HandleFunc("/approve", ...) }),
//	    server.WithHandlerWrap(cors),
//	)
func WithHandlerWrap(wrap func(http.Handler) http.Handler) TransportOption {
	return func(c *transportConfig) { c.handlerWraps = append(c.handlerWraps, wrap) }
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

// IsPublicMethod returns true if the given JSON-RPC method is in the server's
// public method set (configured via WithPublicMethods). Public methods bypass
// auth and are dispatched without claims.
func (s *Server) IsPublicMethod(method string) bool {
	return len(s.options.publicMethods) > 0 && s.options.publicMethods[method]
}

// extractMethodFromJSON extracts the "method" field from a JSON-RPC message
// without full unmarshaling. Returns empty string if extraction fails.
func extractMethodFromJSON(data []byte) string {
	var envelope struct {
		Method string `json:"method"`
	}
	json.Unmarshal(data, &envelope)
	return envelope.Method
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
//
// Cap and rate-limit enforcement also lives here. Counts are tracked per
// sessionID so subscribe() can refuse without scanning the subscribers
// map. Limiters are lazy: created on first metered call and dropped when
// the session unsubscribes all (or disconnects). Per-session bookkeeping
// is the right scope because the issue motivating the backpressure (PR
// 568) is a single client exhausting the registry, not aggregate load.
type subscriptionRegistry struct {
	mu          sync.RWMutex
	subscribers map[string]map[string]*Dispatcher // uri → sessionID → dispatcher
	counts      map[string]int                    // sessionID → live subscription count
	limiters    map[string]*rate.Limiter          // sessionID → token bucket (nil when rate limit disabled)

	// Configured once at construction time; safe to read without the
	// mutex.
	cap       int
	rateLimit rate.Limit
	rateBurst int
	onReject  SubscriptionRejectFunc

	// notificationRelay is the Server's configured Pattern B publisher
	// for cross-replica fan-out. notify() publishes
	// notifications/resources/updated through this on top of
	// notifyLocal() so subscribers on other replicas hear the
	// notification too. nil = local-only behavior (no relay
	// installed). Wired by NewServer from s.options.notificationRelay
	// after both the registry and the option are read.
	notificationRelay NotificationRelay
}

// ErrSubscriptionCapExceeded indicates the calling session has hit
// the per-session concurrent-subscription cap configured via
// [WithSubscriptionCap]. Returned by subscribe so handleResourcesSubscribe
// can map it to the wire error.
var ErrSubscriptionCapExceeded = errors.New("subscription cap exceeded")

// ErrSubscriptionRateLimited indicates the calling session has exceeded
// the per-session subscribe rate configured via
// [WithSubscriptionRateLimit].
var ErrSubscriptionRateLimited = errors.New("subscription rate limited")

// subscribe registers a session's interest in a resource URI. Returns
// nil on success, [ErrSubscriptionCapExceeded] or
// [ErrSubscriptionRateLimited] when refused. Idempotent: subscribing the
// same (session, uri) pair twice succeeds without double-counting.
func (r *subscriptionRegistry) subscribe(sessionID string, d *Dispatcher, uri string) error {
	r.mu.Lock()
	subs, ok := r.subscribers[uri]
	if !ok {
		subs = make(map[string]*Dispatcher)
	}
	_, alreadyHeld := subs[sessionID]

	if !alreadyHeld && r.cap > 0 && r.counts[sessionID] >= r.cap {
		r.mu.Unlock()
		if r.onReject != nil {
			r.onReject(sessionID, uri, "cap_exceeded")
		}
		return fmt.Errorf("%w: session at %d/%d subscriptions", ErrSubscriptionCapExceeded, r.cap, r.cap)
	}

	if !alreadyHeld && r.rateLimit > 0 {
		lim, ok := r.limiters[sessionID]
		if !ok {
			lim = rate.NewLimiter(r.rateLimit, r.rateBurst)
			r.limiters[sessionID] = lim
		}
		if !lim.Allow() {
			r.mu.Unlock()
			if r.onReject != nil {
				r.onReject(sessionID, uri, "rate_limited")
			}
			return fmt.Errorf("%w: %v subscribes/sec, burst %d", ErrSubscriptionRateLimited, r.rateLimit, r.rateBurst)
		}
	}

	if !ok {
		r.subscribers[uri] = subs
	}
	if !alreadyHeld {
		subs[sessionID] = d
		r.counts[sessionID]++
	}
	r.mu.Unlock()
	return nil
}

// unsubscribe removes a session's subscription for a resource URI. No-op
// if the session was not subscribed to the URI.
func (r *subscriptionRegistry) unsubscribe(sessionID, uri string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	subs, ok := r.subscribers[uri]
	if !ok {
		return
	}
	if _, held := subs[sessionID]; !held {
		return
	}
	delete(subs, sessionID)
	if len(subs) == 0 {
		delete(r.subscribers, uri)
	}
	if r.counts[sessionID] > 0 {
		r.counts[sessionID]--
	}
	if r.counts[sessionID] == 0 {
		delete(r.counts, sessionID)
		delete(r.limiters, sessionID)
	}
}

// unsubscribeAll removes all subscriptions for a session (called on disconnect).
// Drops the per-session count and limiter so the session leaves no state
// behind on the registry.
func (r *subscriptionRegistry) unsubscribeAll(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for uri, subs := range r.subscribers {
		delete(subs, sessionID)
		if len(subs) == 0 {
			delete(r.subscribers, uri)
		}
	}
	delete(r.counts, sessionID)
	delete(r.limiters, sessionID)
}

// notifyLocal sends a notifications/resources/updated to all LOCAL
// sessions subscribed to the URI. Used by the cross-replica receive
// path (server.ResourcesUpdatedReceiver.Receive) to deliver
// without re-publishing through the NotificationRelay.
//
// Copies the dispatcher list under read lock, then calls notifyFunc
// outside the lock to avoid holding the lock during potentially slow
// I/O.
func (r *subscriptionRegistry) notifyLocal(uri string) {
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

// notify fires notifyLocal AND publishes via the Server's installed
// NotificationRelay so cross-replica subscribers on other replicas
// receive the same notification. The publish encodes the URI in
// params so receiving replicas can route by URI on their own
// subscriptionRegistry.
//
// Without a relay installed the call is local-only (same as the
// pre-Pattern-B behavior).
func (r *subscriptionRegistry) notify(uri string) {
	r.notifyLocal(uri)
	if r.notificationRelay != nil {
		r.notificationRelay.Publish(
			context.Background(),
			"notifications/resources/updated",
			core.ResourceUpdatedNotification{URI: uri},
		)
	}
}

// NotifyResourceUpdated sends a notifications/resources/updated notification to
// all clients that have subscribed to the given resource URI. This is the
// application-facing API for triggering resource change notifications.
//
// Safe to call from any goroutine. No-op if subscriptions are not enabled or
// no clients are subscribed to the URI.
//
// When a NotificationRelay is installed via WithNotificationRelay, the
// notification reaches subscribers on every replica — the receive
// side (typically a server.ResourcesUpdatedReceiver wired into a
// NotificationRouter) calls notifyLocal on each replica's own
// subscriptionRegistry.
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

// NotifyResourceUpdatedLocal sends a notifications/resources/updated
// notification to LOCAL subscribers only — does NOT publish via the
// installed NotificationRelay. Used by ResourcesUpdatedReceiver.Receive
// to deliver a cross-replica-received notification without
// re-publishing.
//
// External callers should normally use NotifyResourceUpdated.
func (s *Server) NotifyResourceUpdatedLocal(uri string) {
	if s.subRegistry != nil {
		s.subRegistry.notifyLocal(uri)
	}
}
