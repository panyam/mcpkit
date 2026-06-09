package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// HTTPSource is the third source pattern (sibling to YieldingSource and
// TypedSource): events arrive over HTTP from a remote source manager
// rather than via in-process yield calls. The library mounts an
// http.Handler on a caller-supplied mux; the handler decodes inbound
// JSON and forwards into a YieldingSource that the rest of the library
// (push fanout, webhook delivery, events/poll, events/list) already
// knows how to drive.
//
// Use HTTPSource when upstream-integration concerns (Discord WebSocket
// lifecycle, OAuth refresh, polling external APIs) belong in a separate
// process from MCP serving — the canonical example is the push-server
// tier in examples/events/whole-enchilada/. The discord and telegram
// demos use the simpler in-process YieldingSource because their source
// integration runs inside the MCP server process.
//
// HTTPSource satisfies the EventSource interface (Def / Poll / Latest),
// the emitterAware interface (SetEmitHook), and the push-stream
// Subscribe contract via an embedded *YieldingSource[Data]. Callers
// register it the same way as any other source via events.Register,
// and separately mount the Handler() onto an http.ServeMux at
// InjectPath().
//
// Method promotion via the embedded YieldingSource means Def(), Poll(),
// Latest(), Subscribe(), Recent(), ByCursor(), SetMetaFunc() etc. are
// available on *HTTPSource[Data] without explicit delegation.
type HTTPSource[Data any] struct {
	*YieldingSource[Data]
	yield func(context.Context, Data) error
	cfg   HTTPSourceConfig
}

// HTTPSourceConfig configures an HTTPSource at construction time. All
// fields are optional; zero values pick safe defaults documented per
// field.
type HTTPSourceConfig struct {
	// Bearer, when non-empty, requires every inject request to carry
	// an `Authorization: Bearer <Bearer>` header. The check is
	// constant-time-ish — the secret is compared via subtle-equivalent
	// semantics (Go's string == on small inputs is acceptable here
	// because the secret is operator-chosen, not user-controlled).
	// Empty means no bearer enforcement (open inject endpoint).
	Bearer string

	// MaxBodyBytes caps the inject request body size. Requests with a
	// larger Content-Length return 413; bodies that exceed the cap
	// mid-stream return 413 after the read is truncated. Zero means
	// the default of 1 MiB.
	MaxBodyBytes int64

	// InjectPath is the URL path the Handler is mounted at. Zero
	// defaults to "/events/<def.Name>/inject" so a single mux can host
	// many sources without manual path bookkeeping.
	InjectPath string

	// YieldingOpts is forwarded verbatim to the underlying YieldingSource
	// constructor. Use to set WithoutCursors, WithMaxSize, etc.
	YieldingOpts []YieldingOption
}

const defaultHTTPSourceMaxBodyBytes int64 = 1 << 20 // 1 MiB

// NewHTTPSource constructs an HTTPSource that emits events into a
// library-owned YieldingSource. The returned *HTTPSource is safe for
// concurrent use; the embedded YieldingSource handles all locking.
//
// Mount the source's Handler() on an http.ServeMux at InjectPath() and
// register the source with events.Register the same way you would a
// YieldingSource:
//
//	src := events.NewHTTPSource[ChatMessage](chatDef, events.HTTPSourceConfig{
//	    Bearer:       os.Getenv("EVENT_INJECT_BEARER"),
//	    YieldingOpts: []events.YieldingOption{events.WithMaxSize(1000)},
//	})
//	events.Register(events.Config{Sources: []events.EventSource{src}, ...})
//	mux.Handle(src.InjectPath(), src.Handler())
func NewHTTPSource[Data any](def EventDef, cfg HTTPSourceConfig) *HTTPSource[Data] {
	inner, yield := NewYieldingSource[Data](def, cfg.YieldingOpts...)
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultHTTPSourceMaxBodyBytes
	}
	if cfg.InjectPath == "" {
		cfg.InjectPath = "/events/" + def.Name + "/inject"
	}
	return &HTTPSource[Data]{YieldingSource: inner, yield: yield, cfg: cfg}
}

// Yield is an in-process alternative to HTTP inject — a caller that
// holds the *HTTPSource directly can emit events without going through
// HTTP. Useful for tests and for hybrid demos that mix in-process feeds
// with HTTP-fed feeds against the same source.
//
// ctx threads the W3C trace context through into the underlying
// yield (and onward through emit hook → webhook delivery's
// outbound HTTP traceparent header) — SEP-414 P6 (issue 683).
func (s *HTTPSource[Data]) Yield(ctx context.Context, data Data) error { return s.yield(ctx, data) }

// InjectPath returns the URL path the Handler is mounted at. Defaults
// to "/events/<def.Name>/inject" unless overridden in HTTPSourceConfig.
func (s *HTTPSource[Data]) InjectPath() string { return s.cfg.InjectPath }

// Handler returns the http.Handler that decodes inject requests, yields
// the decoded payload into the underlying YieldingSource, and responds
// 202 on success. Error responses use plain text status messages —
// inject is not part of the MCP wire so it does not return JSON-RPC
// envelopes.
//
// HTTP status mapping:
//
//	405 Method Not Allowed — non-POST request.
//	401 Unauthorized       — missing or wrong bearer (when Bearer is set).
//	413 Payload Too Large  — request body exceeds MaxBodyBytes.
//	400 Bad Request        — JSON decode failed.
//	500 Internal Server Error — yield rejected the event (terminated source).
//	202 Accepted           — event was accepted and yielded.
func (s *HTTPSource[Data]) Handler() http.Handler {
	return http.HandlerFunc(s.serveInject)
}

func (s *HTTPSource[Data]) serveInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.cfg.Bearer != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got != s.cfg.Bearer {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	if r.ContentLength > s.cfg.MaxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	defer body.Close()

	raw, err := io.ReadAll(body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	var data Data
	if err := json.Unmarshal(raw, &data); err != nil {
		http.Error(w, "json decode failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	// SEP-414 P6 (issue 683): extract W3C trace context from the
	// inbound HTTP headers and attach to ctx so the underlying yield
	// stamps it onto event.Meta + flows it through the emit hook to
	// any downstream emitter. Closes the round-trip with the
	// outbound traceparent header webhook delivery sets — replica A
	// yields with ctx → webhook out → replica B's serveInject in →
	// yield with the same trace ID.
	ctx := r.Context()
	if tp := r.Header.Get(traceparentHTTPHeader); tp != "" {
		tc := core.TraceContext{
			Traceparent: tp,
			Tracestate:  r.Header.Get(tracestateHTTPHeader),
		}
		ctx = core.WithTraceContext(ctx, tc)
	}

	if err := s.yield(ctx, data); err != nil {
		http.Error(w, "yield failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
