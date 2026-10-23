package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// Watch statuses. A terminated watch was ended upstream (auth revoked, the
// event type removed) and stays listed so the user can see why, until
// Unwatch removes it.
const (
	StatusActive     = "active"
	StatusTerminated = "terminated"
)

// Watch is a standing rule: a subscription upstream plus what to do when it
// fires. It is what the Store persists, Secret included, so a store's backing
// file must be readable by the bridge alone.
type Watch struct {
	ID string `json:"id"`

	// Event is the qualified name, "<server>/<name>". Server and Name are
	// its two halves.
	Event  string `json:"event"`
	Server string `json:"server"`
	Name   string `json:"name"`

	Arguments   map[string]any `json:"arguments,omitempty"`
	Instruction string         `json:"instruction"`

	// Session, when set, is the agent session each occurrence continues
	// instead of starting a fresh run (see ResumeSession).
	Session *Session `json:"session,omitempty"`

	// Tools, when set, is the subset of the sink's tools this rule's runs
	// may use (see WithTools). Sorted, without duplicates.
	Tools []string `json:"tools,omitempty"`

	// Secret signs this watch's deliveries. Reused on resubscribe, so a
	// restart is a refresh of the same subscription upstream.
	Secret string `json:"secret"`

	// Cursor is the resume point after the last event handed to the sink.
	// Nil means "from now".
	Cursor *string `json:"cursor"`

	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	LastError   string    `json:"lastError,omitempty"`
	LastErrorAt time.Time `json:"lastErrorAt,omitzero"`
}

// Session is a Claude Code session a watch continues: its id
// ($CLAUDE_CODE_SESSION_ID inside the session) and the working directory it
// ran in, which is where Claude Code looks for its transcript.
type Session struct {
	ID  string `json:"id"`
	Dir string `json:"dir"`
}

// WatchOption configures one Watch call.
type WatchOption func(*Watch)

// ResumeSession ties a watch to the agent session that created it, so each
// occurrence resumes a fork of that session, with its context, rather than
// starting with nothing but the instruction. See HeadlessClaudeSink for what
// happens when the session no longer exists.
func ResumeSession(id, dir string) WatchOption {
	return func(w *Watch) { w.Session = &Session{ID: id, Dir: dir} }
}

// WithTools limits a watch's runs to the named tools. Every name must be one
// the sink offers (see ToolScoped), so a watch can narrow the daemon's tool
// list and never add to it.
func WithTools(tools ...string) WatchOption {
	return func(w *Watch) {
		if tools == nil {
			tools = []string{}
		}
		w.Tools = tools
	}
}

// ToolScoped is implemented by a sink whose runs are given a tool list, so a
// watch can be limited to part of it. ToolCeiling reports every tool a run
// could get: allowed are given to every run, outward only with approval. The
// bridge checks a watch's tools against it at watch time, and reports the
// effective list in the watch's view.
type ToolScoped interface {
	ToolCeiling() (allowed, outward []string)
}

// validateTools refuses a tool list the sink cannot honour, and returns it
// sorted and deduplicated.
func (b *Bridge) validateTools(tools []string) ([]string, error) {
	if tools == nil {
		return nil, nil
	}
	scoped, ok := b.cfg.Sink.(ToolScoped)
	if !ok {
		return nil, &WatchError{Message: "this bridge's sink takes no per-watch tool list; omit tools"}
	}
	allowed, outward := scoped.ToolCeiling()
	ceiling := sortedUnique(append(append([]string(nil), allowed...), outward...))
	if len(tools) == 0 {
		return nil, &WatchError{Message: "tools is empty; name the tools this rule needs from: " + strings.Join(ceiling, ", ") + ", or omit tools for all of them"}
	}
	var unknown []string
	for _, t := range tools {
		if !contains(ceiling, t) {
			unknown = append(unknown, t)
		}
	}
	if len(unknown) > 0 {
		return nil, &WatchError{Message: fmt.Sprintf("tools %s are not offered by this bridge; a watch may use only: %s",
			strings.Join(sortedUnique(unknown), ", "), strings.Join(ceiling, ", "))}
	}
	return sortedUnique(tools), nil
}

func sortedUnique(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	n := 0
	for i, x := range out {
		if i == 0 || x != out[n-1] {
			out[n] = x
			n++
		}
	}
	return out[:n]
}

var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateSession refuses anything but a UUID and an absolute directory. The
// id ends up in argv after --resume, so a value that could read as a flag
// never gets that far.
func validateSession(s *Session) error {
	if s == nil {
		return nil
	}
	if !sessionIDPattern.MatchString(s.ID) {
		return &WatchError{Message: fmt.Sprintf("session id %q is not a session UUID; pass the value of $CLAUDE_CODE_SESSION_ID", s.ID)}
	}
	if !filepath.IsAbs(s.Dir) {
		return &WatchError{Message: fmt.Sprintf("session dir %q must be an absolute path; pass the session's working directory ($PWD)", s.Dir)}
	}
	return nil
}

// CatalogEntry is one event type as watch_list reports it.
type CatalogEntry struct {
	Event       string   `json:"event"`
	Server      string   `json:"server"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Delivery    []string `json:"delivery,omitempty"`
	InputSchema any      `json:"inputSchema,omitempty"`
}

// WatchError is a refusal the model can act on: Message says what was wrong
// and, where there is one, what to send instead. Details carries schema
// violations from an invalid arguments object.
type WatchError struct {
	Message string                 `json:"message"`
	Details []core.ValidationError `json:"details,omitempty"`
}

func (e *WatchError) Error() string { return e.Message }

// Catalog runs events/list on the named server, or on every server when
// server is empty, and returns the event types whose name or description
// contains every whitespace-separated term of query, case-insensitively.
// Entries are sorted by qualified name. A server that fails to list is
// reported in the returned error alongside whatever the others returned.
func (b *Bridge) Catalog(ctx context.Context, server, query string) ([]CatalogEntry, error) {
	names := make([]string, 0, len(b.upstreams))
	if server != "" {
		if _, ok := b.upstreams[server]; !ok {
			return nil, &WatchError{Message: fmt.Sprintf("unknown server %q; configured servers: %s", server, strings.Join(b.serverNames(), ", "))}
		}
		names = append(names, server)
	} else {
		names = b.serverNames()
	}
	terms := strings.Fields(strings.ToLower(query))
	var out []CatalogEntry
	var failed []string
	for _, name := range names {
		defs, err := listEvents(ctx, b.upstreams[name])
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, d := range defs {
			if !matchesAll(strings.ToLower(d.Name+" "+d.Description), terms) {
				continue
			}
			out = append(out, CatalogEntry{
				Event:       name + "/" + d.Name,
				Server:      name,
				Name:        d.Name,
				Description: d.Description,
				Delivery:    d.Delivery,
				InputSchema: d.InputSchema,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Event < out[j].Event })
	if len(failed) > 0 {
		return out, fmt.Errorf("events/list failed on %s", strings.Join(failed, "; "))
	}
	return out, nil
}

func (b *Bridge) serverNames() []string {
	names := make([]string, 0, len(b.upstreams))
	for n := range b.upstreams {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func matchesAll(haystack string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(haystack, t) {
			return false
		}
	}
	return true
}

// listEvents follows nextCursor until the server stops sending one.
func listEvents(ctx context.Context, up Upstream) ([]events.EventDef, error) {
	var all []events.EventDef
	cursor := ""
	for range 100 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := up.Client.Call(ctx, "events/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Events     []events.EventDef `json:"events"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(res.Raw, &page); err != nil {
			return nil, fmt.Errorf("decode events/list: %w", err)
		}
		all = append(all, page.Events...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("events/list did not finish after 100 pages")
}

// Watch subscribes to a qualified event with arguments, and stores
// instruction to run when it fires. Arguments are checked against the event
// type's inputSchema before anything is sent upstream, so a mistake comes
// back as a WatchError the model can correct rather than a JSON-RPC error
// from the server. The returned Watch is already persisted and live.
func (b *Bridge) Watch(ctx context.Context, event string, arguments map[string]any, instruction string, opts ...WatchOption) (Watch, error) {
	server, name, ok := strings.Cut(event, "/")
	if !ok || server == "" || name == "" {
		return Watch{}, &WatchError{Message: fmt.Sprintf("event %q is not a qualified name; use \"<server>/<event>\" as watch_list returns it", event)}
	}
	if strings.TrimSpace(instruction) == "" {
		return Watch{}, &WatchError{Message: "instruction is required: say what should happen when this event arrives"}
	}
	up, ok := b.upstreams[server]
	if !ok {
		return Watch{}, &WatchError{Message: fmt.Sprintf("unknown server %q; configured servers: %s", server, strings.Join(b.serverNames(), ", "))}
	}
	defs, err := listEvents(ctx, up)
	if err != nil {
		return Watch{}, fmt.Errorf("events/list on %s: %w", server, err)
	}
	var def *events.EventDef
	for i := range defs {
		if defs[i].Name == name {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		return Watch{}, &WatchError{Message: fmt.Sprintf("server %q has no event %q; call watch_list to see what it offers", server, name)}
	}
	if len(def.Delivery) > 0 && !contains(def.Delivery, "webhook") {
		return Watch{}, &WatchError{Message: fmt.Sprintf("event %q offers delivery %v, and the bridge needs webhook", event, def.Delivery)}
	}
	if err := validateArguments(def.InputSchema, arguments); err != nil {
		return Watch{}, err
	}

	w := Watch{
		ID:          newWatchID(),
		Event:       event,
		Server:      server,
		Name:        name,
		Arguments:   arguments,
		Instruction: instruction,
		Secret:      events.GenerateSecret(),
		Status:      StatusActive,
		CreatedAt:   time.Now().UTC(),
	}
	for _, o := range opts {
		o(&w)
	}
	if err := validateSession(w.Session); err != nil {
		return Watch{}, err
	}
	tools, err := b.validateTools(w.Tools)
	if err != nil {
		return Watch{}, err
	}
	w.Tools = tools
	b.mu.Lock()
	b.watches[w.ID] = &w
	b.mu.Unlock()
	if err := b.run(ctx, w); err != nil {
		b.mu.Lock()
		delete(b.watches, w.ID)
		b.mu.Unlock()
		return Watch{}, fmt.Errorf("subscribe to %s: %w", event, err)
	}
	b.mutate(w.ID, func(*Watch) {})
	return w, nil
}

// Unwatch stops a watch, unsubscribes it upstream, and forgets it. An
// unsubscribe that fails upstream is not an error: the subscription's TTL
// evicts it there once the bridge stops refreshing it.
func (b *Bridge) Unwatch(ctx context.Context, id string) error {
	b.mu.Lock()
	w, ok := b.watches[id]
	lw := b.live[id]
	delete(b.live, id)
	b.mu.Unlock()
	if !ok {
		return &WatchError{Message: fmt.Sprintf("no watch %q; call watches to list them", id)}
	}
	var unsubscribed bool
	if lw != nil {
		if lw.sub != nil {
			if err := lw.sub.StopAndUnsubscribe(ctx); err != nil {
				b.logf("bridge: unsubscribe %s: %v", id, err)
			}
			unsubscribed = true
		}
		lw.stop()
	}
	// A watch that is not live (its resubscribe failed, or the server ended
	// it) has no Subscription to unsubscribe through, though the server may
	// still hold one from before a restart.
	if up, ok := b.upstreams[w.Server]; ok && !unsubscribed {
		params := map[string]any{
			"name":     w.Name,
			"delivery": map[string]any{"url": b.callbackURL(id)},
		}
		if len(w.Arguments) > 0 {
			params["arguments"] = w.Arguments
		}
		if _, err := up.Client.Call(ctx, "events/unsubscribe", params); err != nil {
			b.logf("bridge: unsubscribe %s: %v", id, err)
		}
	}
	b.mu.Lock()
	delete(b.watches, id)
	snapshot := b.snapshotLocked()
	b.mu.Unlock()
	return b.store.Save(snapshot)
}

func validateArguments(schema any, arguments map[string]any) error {
	if schema == nil {
		return nil
	}
	compiled, err := core.CompileSchema(schema)
	if err != nil {
		return fmt.Errorf("event inputSchema does not compile: %w", err)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	if verrs := compiled.ValidateValue(arguments); verrs != nil && len(verrs.Errors) > 0 {
		parts := make([]string, 0, len(verrs.Errors))
		for _, e := range verrs.Errors {
			path := e.Path
			if path == "" {
				path = "/"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", path, e.Message))
		}
		return &WatchError{
			Message: "arguments do not match the event's inputSchema: " + strings.Join(parts, "; "),
			Details: verrs.Errors,
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func newWatchID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "w_" + hex.EncodeToString(buf[:])
}
