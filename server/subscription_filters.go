package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	core "github.com/panyam/mcpkit/core"
)

// Extensible subscriptions/listen filters (panyam/mcpkit#1504).
//
// The 2026-07-28 SubscriptionFilter names three base keys, and extensions
// add their own: SEP-3415 events adds eventsListChanged, the agents SEP
// adds agentsListChanged, ext-tasks adds taskIds. Core knows none of the
// extension keys. Each one is registered here by the extension that
// defines it, and the listen handler asks the registry, key by key,
// whether to honor what the client requested.

// SubscriptionFilterKey defines one key of the subscriptions/listen
// filter: its name, and how the server decides to honor it.
type SubscriptionFilterKey struct {
	// Key is the filter field name, e.g. "eventsListChanged".
	Key string

	// Accept is called once per subscriptions/listen request that names
	// Key, with the request's context and the raw JSON value the client
	// sent. It returns one of three outcomes:
	//
	//   - a grant, which is applied to the stream;
	//   - nil and no error, which declines the key: it is left out of the
	//     acknowledgement and nothing it covers is delivered;
	//   - an error, which refuses the whole listen request. A
	//     *core.MissingCapabilityError becomes -32021 with the required
	//     capabilities in error.data, which is what ext-tasks asks for
	//     when a client wants taskIds without declaring the extension.
	//     Any other error becomes -32602 carrying its message.
	//
	// ctx carries the HTTP request's context (so auth claims set by HTTP
	// middleware are visible) and the request's _meta envelope via
	// core.RequestMetaFromContext. That is where a key like taskIds
	// checks, per ID, that the caller may see what it asks for.
	Accept func(ctx context.Context, requested json.RawMessage) (*SubscriptionFilterGrant, error)
}

// SubscriptionFilterGrant is what the server agreed to for one filter key
// on one stream.
type SubscriptionFilterGrant struct {
	// Echo is the value sent back under Key in the acknowledgement. For a
	// list-changed switch it is true. For an ID list it is the accepted
	// subset, which may be smaller than what was requested.
	Echo any

	// Match reports whether a notification belongs to this grant. params
	// is the notification's params as JSON (null when it has none). Match
	// runs on the broadcast path for every open stream, so keep it cheap
	// and non-blocking.
	Match func(method string, params json.RawMessage) bool
}

// ListChangedFilterKey returns the common shape: a boolean key that,
// when the client sends true, delivers one notification method. Any
// other value is declined.
//
//	srv.RegisterSubscriptionFilter(server.ListChangedFilterKey(
//	    "eventsListChanged", "notifications/events/list_changed"))
func ListChangedFilterKey(key, method string) SubscriptionFilterKey {
	return SubscriptionFilterKey{
		Key: key,
		Accept: func(_ context.Context, requested json.RawMessage) (*SubscriptionFilterGrant, error) {
			var on bool
			if json.Unmarshal(requested, &on) != nil || !on {
				return nil, nil
			}
			return &SubscriptionFilterGrant{
				Echo:  true,
				Match: func(m string, _ json.RawMessage) bool { return m == method },
			}, nil
		},
	}
}

// ErrSubscriptionFilterKeyTaken is returned by RegisterSubscriptionFilter
// when the key is already registered, including the three base keys.
var ErrSubscriptionFilterKeyTaken = errors.New("subscription filter key already registered")

// RegisterSubscriptionFilter adds a key to the subscriptions/listen
// filter this server understands. Extensions call it from their own
// Register function. A key may be registered once, and the base keys
// (toolsListChanged, promptsListChanged, resourcesListChanged) are
// reserved. Safe to call while streams are open; it affects streams
// opened afterwards.
//
// Only the 2026-07-28 stateless wire reads the filter. The legacy
// session wire has no subscriptions/listen and delivers every broadcast
// on its GET stream as before.
func (s *Server) RegisterSubscriptionFilter(k SubscriptionFilterKey) error {
	if k.Key == "" || k.Accept == nil {
		return errors.New("subscription filter key needs a Key and an Accept func")
	}
	return s.subFilters.register(k)
}

// subscriptionFilterRegistry maps filter keys to their definitions.
type subscriptionFilterRegistry struct {
	mu   sync.RWMutex
	keys map[string]SubscriptionFilterKey
}

func (r *subscriptionFilterRegistry) register(k SubscriptionFilterKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = map[string]SubscriptionFilterKey{}
	}
	if _, taken := r.keys[k.Key]; taken || isBaseFilterKey(k.Key) {
		return fmt.Errorf("%w: %q", ErrSubscriptionFilterKeyTaken, k.Key)
	}
	r.keys[k.Key] = k
	return nil
}

func (r *subscriptionFilterRegistry) lookup(key string) (SubscriptionFilterKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[key]
	return k, ok
}

func isBaseFilterKey(key string) bool {
	switch key {
	case core.SubscriptionFilterToolsListChanged,
		core.SubscriptionFilterPromptsListChanged,
		core.SubscriptionFilterResourcesListChanged:
		return true
	}
	return false
}

// baseFilterKey returns the definition of a base key, honored only when
// the server advertises the matching listChanged capability. The schema
// says the acknowledgement omits a type the server does not support, and
// gives promptsListChanged on a server with no prompts as the example.
func baseFilterKey(key string, caps core.ServerCapabilities) (SubscriptionFilterKey, bool) {
	var method string
	var supported bool
	switch key {
	case core.SubscriptionFilterToolsListChanged:
		method = "notifications/tools/list_changed"
		supported = caps.Tools != nil && caps.Tools.ListChanged
	case core.SubscriptionFilterPromptsListChanged:
		method = "notifications/prompts/list_changed"
		supported = caps.Prompts != nil && caps.Prompts.ListChanged
	case core.SubscriptionFilterResourcesListChanged:
		method = "notifications/resources/list_changed"
		supported = caps.Resources != nil && caps.Resources.ListChanged
	default:
		return SubscriptionFilterKey{}, false
	}
	if !supported {
		return SubscriptionFilterKey{Key: key, Accept: declineFilter}, true
	}
	return ListChangedFilterKey(key, method), true
}

func declineFilter(context.Context, json.RawMessage) (*SubscriptionFilterGrant, error) {
	return nil, nil
}

// acceptedFilter is the outcome of resolving one requested filter: what
// to echo, and the matchers to run on the broadcast path.
type acceptedFilter struct {
	honored core.SubscriptionFilter
	matches []func(method string, params json.RawMessage) bool
}

// admits reports whether any honored key covers the notification.
func (a acceptedFilter) admits(method string, params json.RawMessage) bool {
	for _, m := range a.matches {
		if m(method, params) {
			return true
		}
	}
	return false
}

// resolveFilter runs each requested key through its definition. Unknown
// keys and declined keys are dropped silently, as the schema asks. The
// first Accept error refuses the request and is returned as is. Keys are
// visited in sorted order so Accept calls, and which error wins, are
// stable.
func (s *Server) resolveFilter(ctx context.Context, requested core.SubscriptionFilter, caps core.ServerCapabilities) (acceptedFilter, error) {
	out := acceptedFilter{honored: core.SubscriptionFilter{}}
	names := make([]string, 0, len(requested))
	for k := range requested {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		def, ok := baseFilterKey(name, caps)
		if !ok {
			def, ok = s.subFilters.lookup(name)
		}
		if !ok {
			continue
		}
		grant, err := def.Accept(ctx, requested[name])
		if err != nil {
			return acceptedFilter{}, err
		}
		if grant == nil || grant.Match == nil {
			continue
		}
		echo, err := json.Marshal(grant.Echo)
		if err != nil {
			continue
		}
		out.honored[name] = echo
		out.matches = append(out.matches, grant.Match)
	}
	return out, nil
}

// filterRefusal turns an Accept error into the listen response.
func filterRefusal(id json.RawMessage, err error) *core.Response {
	var missing *core.MissingCapabilityError
	if errors.As(err, &missing) {
		return core.NewErrorResponseWithData(id, core.ErrCodeMissingRequiredClientCapability, missing.Error(),
			core.MissingRequiredClientCapabilityData{RequiredCapabilities: missing.Required})
	}
	return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "subscriptions/listen filter refused: "+err.Error())
}
