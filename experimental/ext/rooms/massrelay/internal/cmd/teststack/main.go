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

	bridge := rooms.New(massrelay.Embedded(relay.Service, &services.Principal{Subject: "bridge", ClientID: relaytest.ClientID}),
		rooms.WithCallTimeout(*callTimeout))
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

	srv := server.NewServer(core.ServerInfo{Name: "rooms-teststack", Version: "0"}, server.WithAuth(agents))
	bridge.Register(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()
	info.MCP = ts.URL + "/mcp"

	if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
		panic(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
