package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// sourceAdmin owns the HTTP admin API for runtime source registration
// on this replica. Hooks into the events.Registry for the
// AddSource/RemoveSource calls and tracks the active dynamic adapters
// separately so RemoveSource also tears down the upstream connection
// (Discord WebSocket session, etc.).
//
// Per-replica state — no cross-replica coordination. The CLI fans out
// to specific replicas via path-routed nginx (/admin/replicas/{idx}/);
// each replica only sees its own active adapters. This is a deliberate
// demo posture; production source-of-truth lives in something
// operator-managed (an etcd / Consul registry, an admin UI, etc.).
//
// In-memory only — adapter configs are lost on event-server restart.
// Operator re-issues the evctl add calls. The TopologySourceName
// stream observed by subscribers reflects the live state per replica.
type sourceAdmin struct {
	reg *events.Registry

	mu     sync.Mutex
	active map[string]dynamicSource // name → live adapter
}

// dynamicSource is the minimum surface a runtime-added adapter needs
// to expose so sourceAdmin can list it, look it up for removal, and
// tear down its upstream connection. Both the Discord adapter and any
// future Telegram adapter satisfy it.
type dynamicSource interface {
	Name() string
	close() error
	descriptor() sourceDescriptor // for GET /admin/sources
}

func newSourceAdmin(reg *events.Registry) *sourceAdmin {
	return &sourceAdmin{
		reg:    reg,
		active: make(map[string]dynamicSource),
	}
}

// Handler returns the http.Handler that mounts the /admin/sources/*
// routes. main.go calls this inside the WithMux block.
func (a *sourceAdmin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/sources/discord", a.addDiscord)
	mux.HandleFunc("DELETE /admin/sources/{name}", a.remove)
	mux.HandleFunc("GET /admin/sources", a.list)
	return mux
}

// shutdown closes every active adapter + unregisters it from the
// Registry. Called from main.go's defer so a clean event-server exit
// doesn't leak Discord WebSocket sessions.
func (a *sourceAdmin) shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, ds := range a.active {
		if err := ds.close(); err != nil {
			log.Printf("[admin] shutdown: close %q: %v", name, err)
		}
		if err := a.reg.RemoveSource(name); err != nil {
			log.Printf("[admin] shutdown: RemoveSource %q: %v", name, err)
		}
	}
	a.active = nil
}

// --- POST /admin/sources/discord ----------------------------------

type addDiscordRequest struct {
	BotToken   string   `json:"bot_token"`
	ChannelIDs []string `json:"channel_ids"`
	Tenants    []string `json:"tenants,omitempty"`
	Name       string   `json:"name,omitempty"` // defaults to "discord.message"
}

func (a *sourceAdmin) addDiscord(w http.ResponseWriter, r *http.Request) {
	var req addDiscordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg := DiscordSourceConfig{
		BotToken:   req.BotToken,
		ChannelIDs: req.ChannelIDs,
		Tenants:    req.Tenants,
		SourceName: req.Name,
	}
	ds, err := newDiscordSource(cfg)
	if err != nil {
		http.Error(w, "discord adapter: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Open the gateway BEFORE AddSource so the source is producing
	// events the moment subscribers can find it.
	if err := ds.open(); err != nil {
		http.Error(w, "discord open: "+err.Error(), http.StatusBadGateway)
		return
	}

	a.mu.Lock()
	if _, exists := a.active[ds.Name()]; exists {
		a.mu.Unlock()
		_ = ds.close()
		http.Error(w, fmt.Sprintf("source %q already active", ds.Name()), http.StatusConflict)
		return
	}
	if err := a.reg.AddSource(ds.Source()); err != nil {
		a.mu.Unlock()
		_ = ds.close()
		http.Error(w, "AddSource: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.active[ds.Name()] = newDiscordDescriptor(ds)
	a.mu.Unlock()

	log.Printf("[admin] discord adapter added: name=%s channels=%v tenants=%v",
		ds.Name(), cfg.ChannelIDs, cfg.Tenants)

	writeJSON(w, http.StatusOK, a.active[ds.Name()].descriptor())
}

// --- DELETE /admin/sources/{name} ---------------------------------

func (a *sourceAdmin) remove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "missing source name", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	ds, ok := a.active[name]
	if ok {
		delete(a.active, name)
	}
	a.mu.Unlock()
	if !ok {
		http.Error(w, fmt.Sprintf("source %q not active", name), http.StatusNotFound)
		return
	}
	// Order: RemoveSource first so the Registry stops routing to it,
	// then close the upstream connection. Reverse-order risks the
	// gateway delivering a final message into a removed source slot.
	regErr := a.reg.RemoveSource(name)
	closeErr := ds.close()
	if regErr != nil {
		log.Printf("[admin] remove %q: RemoveSource: %v", name, regErr)
	}
	if closeErr != nil {
		log.Printf("[admin] remove %q: close: %v", name, closeErr)
	}
	if err := errors.Join(regErr, closeErr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[admin] source removed: name=%s", name)
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /admin/sources -------------------------------------------

type listResponse struct {
	Sources []sourceDescriptor `json:"sources"`
}

// sourceDescriptor is the shape echoed back in GET /admin/sources and
// the add response. Static / meta sources have only Name + Type; the
// dynamic Discord adapter adds its config (sans bot token).
type sourceDescriptor struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"` // "discord" | "static" | "meta"
	ChannelIDs []string `json:"channel_ids,omitempty"`
	Tenants    []string `json:"tenants,omitempty"`
}

func (a *sourceAdmin) list(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	activeCopy := make(map[string]sourceDescriptor, len(a.active))
	for name, ds := range a.active {
		activeCopy[name] = ds.descriptor()
	}
	a.mu.Unlock()

	allNames := a.reg.SourceNames()
	out := make([]sourceDescriptor, 0, len(allNames))
	for _, name := range allNames {
		if d, ok := activeCopy[name]; ok {
			out = append(out, d)
			continue
		}
		out = append(out, sourceDescriptor{
			Name: name,
			Type: classifyStaticSource(name),
		})
	}
	writeJSON(w, http.StatusOK, listResponse{Sources: out})
}

// classifyStaticSource labels a Registry-known source that the admin
// API didn't register. events.topology is the reserved meta-source;
// everything else is "static" — registered at boot via cfg.Sources.
func classifyStaticSource(name string) string {
	if strings.HasPrefix(name, "events.") {
		return "meta"
	}
	return "static"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[admin] encode response: %v", err)
	}
}

// --- adapter-side glue --------------------------------------------

// discordDescriptor adapts *discordSource to the dynamicSource
// interface so sourceAdmin can speak to it uniformly. Defined here
// rather than on discordSource itself so the adapter file stays
// agnostic to the admin layer's shape.
type discordDescriptor struct {
	src *discordSource
}

func newDiscordDescriptor(src *discordSource) dynamicSource {
	return &discordDescriptor{src: src}
}

func (d *discordDescriptor) Name() string  { return d.src.Name() }
func (d *discordDescriptor) close() error  { return d.src.close() }
func (d *discordDescriptor) descriptor() sourceDescriptor {
	return sourceDescriptor{
		Name:       d.src.cfg.SourceName,
		Type:       "discord",
		ChannelIDs: d.src.cfg.ChannelIDs,
		Tenants:    d.src.cfg.Tenants,
	}
}
