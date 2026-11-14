package server

import (
	"context"
	"sync/atomic"

	"github.com/panyam/mcpkit/core"
)

// ListScope hides registered primitives from one caller's tools/list,
// prompts/list and resources/list, while leaving them registered and callable.
//
// An extension installs one when the set a caller discovers depends on who is
// asking. The groups draft (progressive-disclosure-wg PR 19) is the first
// user: a client that declares groups gets a tool only through groups/get, and
// a client that doesn't gets the flat list.
//
// Hiding is discovery only. tools/call, prompts/get and resources/read still
// serve a hidden primitive, so anything that needs protecting needs
// authorization as well.
//
// The methods run on every list call, on both wires, and may run
// concurrently. ctx carries the caller's session and request _meta, so
// core.ClientSupportsExtension answers the same way on either wire.
type ListScope interface {
	HideTool(ctx context.Context, name string) bool
	HidePrompt(ctx context.Context, name string) bool
	HideResource(ctx context.Context, uri string) bool
}

// SetListScope installs ls for every list call that follows, in every
// session, including sessions that started earlier. nil removes it.
//
// With a scope installed, tools/list, prompts/list and resources/list carry
// cacheScope "private" wherever they carry a cacheScope at all. The lists
// depend on the caller now, and a shared cache would hand one caller's list
// to another. resources/templates/list is not filtered.
func (s *Server) SetListScope(ls ListScope) {
	s.dispatcher.listScope.set(ls)
}

// listScopeRef is shared by a server's dispatcher and every session
// dispatcher cloned from it, so SetListScope reaches all of them.
type listScopeRef struct {
	v atomic.Pointer[listScopeBox]
}

type listScopeBox struct{ ls ListScope }

func (r *listScopeRef) set(ls ListScope) {
	if ls == nil {
		r.v.Store(nil)
		return
	}
	r.v.Store(&listScopeBox{ls})
}

func (r *listScopeRef) get() ListScope {
	if r == nil {
		return nil
	}
	if b := r.v.Load(); b != nil {
		return b.ls
	}
	return nil
}

// scopedListHints is listCacheHints for a list a ListScope may filter.
func (d *Dispatcher) scopedListHints(wire wireKind) (*int, string) {
	ttlMs, scope := d.listCacheHints(wire)
	if scope != "" && d.listScope.get() != nil {
		scope = core.CacheScopePrivate
	}
	return ttlMs, scope
}

// visible drops the items hide reports, keeping order. With no scope it
// returns items unchanged.
func visible[T any](items []T, hide func(T) bool) []T {
	if hide == nil {
		return items
	}
	out := items[:0:0]
	for _, it := range items {
		if !hide(it) {
			out = append(out, it)
		}
	}
	return out
}
