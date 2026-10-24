// Command teststack runs the room bridge on a real massrelay relay for the
// TypeScript page client's tests: a relay requiring JWTs, the bridge joined
// embedded, and an MCP endpoint for agents. It prints one JSON line with the
// URLs and tokens, then serves until its standard input closes.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/panyam/massrelay/services"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay/internal/relaytest"
	"github.com/panyam/mcpkit/server"
)

// tokens maps an agent bearer token to the claims a JWT validator would give.
type tokens map[string]*core.Claims

func (v tokens) Validate(r *http.Request) error {
	if v.Claims(r) == nil {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unknown token"}
	}
	return nil
}

func (v tokens) Claims(r *http.Request) *core.Claims {
	return v[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
}

// Info is what the stack prints for the tests.
type Info struct {
	Relay       string            `json:"relay"`
	MCP         string            `json:"mcp"`
	PageTokens  map[string]string `json:"pageTokens"`
	AgentTokens map[string]string `json:"agentTokens"`
}

func main() {
	callTimeout := flag.Duration("call-timeout", 30*time.Second, "the bridge's per-call deadline")
	flag.Parse()
	log.SetOutput(io.Discard) // the relay logs every frame; stdout carries Info

	relay, err := relaytest.Start()
	if err != nil {
		panic(err)
	}
	defer relay.Close()

	// Page events the bridge hands its hook, for the TypeScript tests to read
	// back through the teststack_page_events tool.
	var eventsMu sync.Mutex
	var pageEvents []rooms.PageEvent
	recordEvent := func(e rooms.PageEvent) {
		eventsMu.Lock()
		pageEvents = append(pageEvents, e)
		eventsMu.Unlock()
	}

	bridge := rooms.New(massrelay.Embedded(relay.Service, &services.Principal{Subject: "bridge", ClientID: relaytest.ClientID}),
		rooms.WithCallTimeout(*callTimeout), rooms.WithPageEvents(recordEvent), rooms.WithEventFallbacks(0))
	defer bridge.Close()
	scopes := []string{rooms.ScopeRoomsAttach, rooms.ScopePagesRead, rooms.ScopePagesAct}
	agents := tokens{}
	info := Info{Relay: relay.URL, PageTokens: map[string]string{}, AgentTokens: map[string]string{}}
	for _, subject := range []string{"alice", "bob"} {
		tok, err := relaytest.Token(subject, nil)
		if err != nil {
			panic(err)
		}
		info.PageTokens[subject] = tok
		agentTok := "agent-" + subject
		agents[agentTok] = &core.Claims{Subject: subject, Scopes: scopes}
		info.AgentTokens[subject] = agentTok
	}

	srv := server.NewServer(core.ServerInfo{Name: "rooms-teststack", Version: "0"}, server.WithAuth(agents), server.WithSubscriptions())
	bridge.Register(srv)
	srv.RegisterTool(core.ToolDef{
		Name:        "teststack_page_events",
		Description: "Test only: every page event the bridge's hook received, in order.",
		InputSchema: map[string]any{"type": "object"},
	}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		out := make([]map[string]any, 0, len(pageEvents))
		for _, e := range pageEvents {
			out = append(out, map[string]any{"resource": e.Resource, "page": string(e.Page), "subject": e.Subject,
				"label": e.Label, "name": e.Name, "data": e.Data})
		}
		return core.StructuredResult("page events", map[string]any{"events": out}), nil
	})
	srv.RegisterTool(core.ToolDef{
		Name:        "teststack_notify",
		Description: "Test only: the host's Bridge.Notify, with {rooms, users, name, data}.",
		InputSchema: map[string]any{"type": "object"},
	}, func(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		var a struct {
			Rooms []string        `json:"rooms"`
			Users []string        `json:"users"`
			Name  string          `json:"name"`
			Data  json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(req.Arguments, &a)
		rep, err := bridge.Notify(tc, rooms.Selector{Rooms: a.Rooms, Users: a.Users}, a.Name, a.Data)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		return core.StructuredResult("notified", map[string]any{"sent": len(rep.Sent)}), nil
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()
	info.MCP = ts.URL + "/mcp"

	if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
		panic(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
