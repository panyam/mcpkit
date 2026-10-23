// Package bridge lets an agent subscribe to MCP events and have them acted on
// while nobody is watching (#1579).
//
// A Bridge is an MCP server facing the host (Claude Code, or any MCP client)
// and an events client of one or more upstream events servers. It offers four
// tools that work across every event type and server: watch_list, watch,
// unwatch and watches. A watch is a webhook subscription upstream, with the
// bridge as the receiver, plus an instruction from the user. When an event
// arrives the bridge hands it, with the instruction, to an EventSink; the
// shipped one is HeadlessClaudeSink, which starts a headless `claude -p` run.
//
// Watches, their secrets and their cursors persist through a Store, so a
// standing rule outlives both the chat session that made it and a restart of
// the bridge.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// HookPathPrefix is where the bridge's webhook handler expects deliveries.
// Each watch gets its own path under it, so a delivery is routed by URL and
// verified with that watch's own secret.
const HookPathPrefix = "/hooks/"

// Upstream is one events server the bridge can watch. Name qualifies the
// server's event names (`<name>/<event>`) and must not contain "/". Client
// must already be connected.
type Upstream struct {
	Name   string
	Client *client.Client
}

// Config configures New.
type Config struct {
	// Upstreams are the events servers watch_list searches and watch
	// subscribes on. At least one is required.
	Upstreams []Upstream

	// PublicURL is the base URL upstream servers POST deliveries to, without
	// a trailing slash, e.g. "https://bridge.example.com". The bridge's
	// Handler must be reachable at PublicURL + HookPathPrefix.
	PublicURL string

	// Sink receives every delivered event. Required.
	Sink eventsclient.EventSink

	// Store persists watches. Nil keeps them in memory only, which loses
	// every standing rule on restart.
	Store Store

	// Dedup is shared by every watch's receiver. Nil gives the bridge its
	// own.
	Dedup *eventsclient.Deduper

	// Logf reports delivery and resubscribe failures. Nil uses log.Printf.
	Logf func(format string, args ...any)

	// Approvals, when set, gives the bridge's MCP server the approvals,
	// approve and deny tools over it. Set the same PendingApprover as the
	// sink's Approver (HeadlessClaudeSink.Approver), so the requests its runs
	// park are the ones these tools answer.
	Approvals *PendingApprover
}

// Bridge holds the watches and their live subscriptions. Construct with New,
// call Start to resume persisted watches, serve Handler for webhooks, and
// Register to expose the tools.
type Bridge struct {
	cfg       Config
	upstreams map[string]Upstream
	store     Store
	dedup     *eventsclient.Deduper
	logf      func(string, ...any)

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	watches map[string]*Watch
	live    map[string]*liveWatch
}

// liveWatch is the running half of a Watch: its receiver, its upstream
// subscription and the loop handing events to the sink.
type liveWatch struct {
	recv   *eventsclient.Receiver[json.RawMessage]
	sub    *eventsclient.Subscription
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates cfg and returns a Bridge with no watches running.
func New(cfg Config) (*Bridge, error) {
	if len(cfg.Upstreams) == 0 {
		return nil, errors.New("bridge: at least one upstream is required")
	}
	if cfg.Sink == nil {
		return nil, errors.New("bridge: Sink is required")
	}
	if cfg.PublicURL == "" {
		return nil, errors.New("bridge: PublicURL is required")
	}
	ups := make(map[string]Upstream, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		if u.Name == "" || strings.Contains(u.Name, "/") {
			return nil, fmt.Errorf("bridge: upstream name %q must be non-empty and contain no '/'", u.Name)
		}
		if u.Client == nil {
			return nil, fmt.Errorf("bridge: upstream %q has no client", u.Name)
		}
		if _, dup := ups[u.Name]; dup {
			return nil, fmt.Errorf("bridge: upstream %q configured twice", u.Name)
		}
		ups[u.Name] = u
	}
	store := cfg.Store
	if store == nil {
		store = NewMemoryStore()
	}
	dedup := cfg.Dedup
	if dedup == nil {
		dedup = eventsclient.NewDeduper(0, 0)
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{
		cfg:       cfg,
		upstreams: ups,
		store:     store,
		dedup:     dedup,
		logf:      logf,
		ctx:       ctx,
		cancel:    cancel,
		watches:   make(map[string]*Watch),
		live:      make(map[string]*liveWatch),
	}, nil
}

// Start loads the persisted watches and resubscribes every active one with
// its stored secret and cursor, so events that arrived upstream while the
// bridge was down are delivered from the cursor on. A watch that cannot be
// resubscribed stays listed with its error in LastError rather than failing
// Start, so one dead upstream does not take every other rule down with it.
func (b *Bridge) Start(ctx context.Context) error {
	saved, err := b.store.Load()
	if err != nil {
		return fmt.Errorf("bridge: load watches: %w", err)
	}
	b.mu.Lock()
	for i := range saved {
		w := saved[i]
		b.watches[w.ID] = &w
	}
	b.mu.Unlock()
	for _, w := range b.List() {
		if w.Status != StatusActive {
			continue
		}
		if err := b.run(ctx, w); err != nil {
			b.setError(w.ID, err)
			b.logf("bridge: resubscribe %s (%s): %v", w.ID, w.Event, err)
		}
	}
	return nil
}

// Close stops every subscription's refresh loop and every delivery loop. It
// does not unsubscribe upstream, because a restarted bridge resumes the same
// watches; Unwatch is what removes one.
func (b *Bridge) Close() {
	b.cancel()
	b.mu.Lock()
	live := b.live
	b.live = make(map[string]*liveWatch)
	b.mu.Unlock()
	for _, lw := range live {
		lw.stop()
	}
}

// Handler serves webhook deliveries for every watch, at
// HookPathPrefix + <watch id>. Mount it on the mux at HookPathPrefix.
func (b *Bridge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, HookPathPrefix)
		b.mu.Lock()
		lw := b.live[id]
		b.mu.Unlock()
		if lw == nil {
			http.Error(w, "no such watch", http.StatusNotFound)
			return
		}
		lw.recv.ServeHTTP(w, r)
	})
}

// List returns every watch, sorted by id.
func (b *Bridge) List() []Watch {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Watch, 0, len(b.watches))
	for _, w := range b.watches {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *Bridge) callbackURL(id string) string {
	return b.cfg.PublicURL + HookPathPrefix + id
}

// run starts a watch's receiver and subscription. The receiver is reachable
// before Subscribe is sent, because the upstream's endpoint-verification
// challenge arrives while Subscribe is still in flight.
func (b *Bridge) run(ctx context.Context, w Watch) error {
	up, ok := b.upstreams[w.Server]
	if !ok {
		return fmt.Errorf("server %q is not configured on this bridge", w.Server)
	}
	recv := eventsclient.NewReceiver[json.RawMessage](w.Secret, eventsclient.WithDeduper(b.dedup))
	lwCtx, cancel := context.WithCancel(b.ctx)
	lw := &liveWatch{recv: recv, ctx: lwCtx, cancel: cancel, done: make(chan struct{})}
	recv.OnGap(func(cursor string) { b.setCursor(w.ID, &cursor) })
	recv.OnTerminated(func(err eventsclient.ControlError) {
		b.logf("bridge: watch %s terminated upstream: %d %s", w.ID, err.Code, err.Message)
		go b.terminate(w.ID, fmt.Sprintf("terminated upstream: %d %s", err.Code, err.Message))
	})

	b.mu.Lock()
	b.live[w.ID] = lw
	b.mu.Unlock()

	sub, err := eventsclient.Subscribe(b.ctx, up.Client, eventsclient.SubscribeOptions{
		EventName:   w.Name,
		CallbackURL: b.callbackURL(w.ID),
		Secret:      w.Secret,
		Cursor:      w.Cursor,
		Arguments:   w.Arguments,
	})
	if err != nil {
		b.mu.Lock()
		delete(b.live, w.ID)
		b.mu.Unlock()
		cancel()
		recv.Close()
		return err
	}
	lw.sub = sub
	go b.deliverLoop(w, lw)
	return nil
}

// deliverLoop hands one watch's events to the sink in arrival order, and
// advances the persisted cursor after each, so a restart resumes after the
// last event the sink was given.
func (b *Bridge) deliverLoop(w Watch, lw *liveWatch) {
	defer close(lw.done)
	for ev := range lw.recv.Events() {
		d := eventsclient.Delivery{
			Server:         w.Server,
			Instruction:    w.Instruction,
			SubscriptionID: ev.SubscriptionID,
			Event: events.Event{
				EventID:   ev.EventID,
				Name:      ev.Name,
				Timestamp: ev.Timestamp,
				Data:      ev.Data,
				Cursor:    ev.Cursor,
				Meta:      ev.Meta,
			},
		}
		if w.Session != nil {
			d.Session, d.SessionDir = w.Session.ID, w.Session.Dir
		}
		d.Tools = w.Tools
		if err := b.cfg.Sink.Deliver(lw.ctx, d); err != nil {
			b.logf("bridge: watch %s: sink: %v", w.ID, err)
			b.setError(w.ID, fmt.Errorf("event %s: %w", ev.EventID, err))
		}
		if ev.Cursor != nil {
			b.setCursor(w.ID, ev.Cursor)
		}
	}
}

// stop cancels a sink run in progress, ends the refresh loop and closes the
// receiver, then waits for the delivery loop to drain.
func (lw *liveWatch) stop() {
	lw.cancel()
	if lw.sub != nil {
		lw.sub.Stop()
	}
	lw.recv.Close()
	if lw.sub != nil {
		<-lw.done
	}
}

func (b *Bridge) setCursor(id string, cursor *string) {
	b.mutate(id, func(w *Watch) { w.Cursor = cursor })
}

func (b *Bridge) setError(id string, err error) {
	b.mutate(id, func(w *Watch) {
		w.LastError = err.Error()
		w.LastErrorAt = time.Now().UTC()
	})
}

func (b *Bridge) terminate(id, reason string) {
	b.mu.Lock()
	lw := b.live[id]
	delete(b.live, id)
	b.mu.Unlock()
	if lw != nil {
		lw.stop()
	}
	b.mutate(id, func(w *Watch) {
		w.Status = StatusTerminated
		w.LastError = reason
		w.LastErrorAt = time.Now().UTC()
	})
}

// mutate applies fn to a watch and persists the result. A failed save is
// logged rather than returned: the in-memory state is still right, and the
// next successful save carries the change.
func (b *Bridge) mutate(id string, fn func(*Watch)) {
	b.mu.Lock()
	w, ok := b.watches[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	fn(w)
	snapshot := b.snapshotLocked()
	b.mu.Unlock()
	if err := b.store.Save(snapshot); err != nil {
		b.logf("bridge: save watches: %v", err)
	}
}

func (b *Bridge) snapshotLocked() []Watch {
	out := make([]Watch, 0, len(b.watches))
	for _, w := range b.watches {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
