// Example: Lanternfall, a small map game whose game master is an AI agent.
//
// The page runs in the player's own browser tab, not in a chat host. An agent
// (Claude Code, or one built on any agent SDK) attaches to the game's room
// through the room bridge, calls the tools the page offers, and hears what
// the player does there as events.
//
// Run:  go run . -addr :8080
// Open: http://localhost:8080/signin?as=alice&game=demo
// Then point an agent at http://localhost:8080/mcp with the bearer token
// player:alice (see README.md).
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/experimental/integrations/roomsevents"
	"github.com/panyam/mcpkit/server"
)

// catalog.json declares the page's tools and event types. The page imports
// the same file, so the bridge's fixed catalog and what the page offers
// cannot drift apart.
//
//go:embed catalog.json
var catalogJSON []byte

//go:embed static
var staticFiles embed.FS

const (
	playerCookie = "lanternfall_player"
	// bridgeSubject is the relay subject the bridge joins as. Pages trust
	// only it, through the join response.
	bridgeSubject = "lanternfall-bridge"
)

var namePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

const gameInstructions = `

This server is Lanternfall, a small map game. You are its game master. A game's room is the resource lanternfall:<game>, such as lanternfall:demo; the player tells you which game. Each player's tab is their own map: look before you describe a place, reveal details and hide items to make the place feel alive, and answer what the player asks from the page with narrate.`

type catalog struct {
	Tools []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
		ReadOnly    bool           `json:"readOnly"`
	} `json:"tools"`
	Events []struct {
		Name          string         `json:"name"`
		Description   string         `json:"description"`
		PayloadSchema map[string]any `json:"payloadSchema"`
		ContextHint   map[string]any `json:"contextHint"`
	} `json:"events"`
}

func loadCatalog() ([]rooms.PageToolDecl, []rooms.PageEventDecl) {
	var c catalog
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		log.Fatalf("catalog.json: %v", err)
	}
	var tools []rooms.PageToolDecl
	for _, t := range c.Tools {
		tools = append(tools, rooms.PageToolDecl{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, ReadOnly: t.ReadOnly})
	}
	var evs []rooms.PageEventDecl
	for _, e := range c.Events {
		evs = append(evs, rooms.PageEventDecl{Name: e.Name, Description: e.Description, PayloadSchema: e.PayloadSchema, ContextHint: e.ContextHint})
	}
	return tools, evs
}

// playerTokens is the demo's agent auth: the bearer token player:<name> acts
// for <name>. Fine on localhost, and nowhere else.
type playerTokens struct{}

func (p playerTokens) Validate(r *http.Request) error {
	if p.Claims(r) == nil {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "use the bearer token player:<name>"}
	}
	return nil
}

func (playerTokens) Claims(r *http.Request) *core.Claims {
	name, ok := strings.CutPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "player:")
	if !ok || !namePattern.MatchString(name) {
		return nil
	}
	return &core.Claims{Subject: name, Scopes: []string{rooms.ScopeRoomsAttach, rooms.ScopePagesRead, rooms.ScopePagesAct}}
}

// newHandler builds the whole app: the MCP endpoint, the relay, the join
// endpoint, sign-in and the page.
func newHandler(logger *log.Logger) (http.Handler, func(), error) {
	tools, evs := loadCatalog()
	local, err := massrelay.NewLocal(massrelay.LocalOptions{BridgeSubject: bridgeSubject})
	if err != nil {
		return nil, nil, err
	}
	ev := roomsevents.New()
	bridge := rooms.New(local.Transport(),
		rooms.WithPageTools(tools),
		rooms.WithPageEventCatalog(evs),
		rooms.WithPageEvents(ev.Publish),
		// next_request, for agents whose host cannot subscribe to events.
		rooms.WithEventFallbacks(0),
	)
	opts := append(common.WithMCPLogging(logger),
		server.WithAuth(playerTokens{}),
		server.WithInstructions(rooms.Instructions+gameInstructions),
		server.WithSubscriptions(),
	)
	srv := server.NewServer(core.ServerInfo{Name: "lanternfall", Version: "0.1.0"}, opts...)
	bridge.Register(srv)
	ev.Register(srv, bridge, events.Config{})

	pages, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	mux.Handle("/relay/", http.StripPrefix("/relay", local.Relay()))
	mux.Handle("POST /rooms/join", local.JoinHandler(rooms.JoinOptions{
		Subject: func(r *http.Request) (string, error) {
			c, err := r.Cookie(playerCookie)
			if err != nil || !namePattern.MatchString(c.Value) {
				return "", errors.New("sign in first, at /signin?as=<name>")
			}
			return c.Value, nil
		},
		RoomOf: func(r *http.Request) (string, error) {
			game := r.URL.Query().Get("game")
			if !namePattern.MatchString(game) {
				return "", errors.New("name a game: lowercase letters, digits and dashes")
			}
			return "lanternfall:" + game, nil
		},
		RelayURL: "/relay",
	}))
	mux.HandleFunc("GET /signin", func(w http.ResponseWriter, r *http.Request) {
		name, game := r.URL.Query().Get("as"), r.URL.Query().Get("game")
		if !namePattern.MatchString(name) {
			http.Error(w, "sign in with ?as=<name>: lowercase letters, digits and dashes", http.StatusBadRequest)
			return
		}
		if !namePattern.MatchString(game) {
			game = "demo"
		}
		http.SetCookie(w, &http.Cookie{Name: playerCookie, Value: name, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/?game="+url.QueryEscape(game), http.StatusSeeOther)
	})
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name := ""
		if c, err := r.Cookie(playerCookie); err == nil && namePattern.MatchString(c.Value) {
			name = c.Value
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"player": name})
	})
	mux.Handle("/", http.FileServerFS(pages))
	return mux, func() { bridge.Close(); local.Close() }, nil
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	logger := common.NewMCPLogger("[mcp] ")
	h, closeFn, err := newHandler(logger)
	if err != nil {
		log.Fatal(err)
	}
	defer closeFn()
	log.Printf("[lanternfall] open http://localhost%s/signin?as=alice&game=demo, MCP at /mcp", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
