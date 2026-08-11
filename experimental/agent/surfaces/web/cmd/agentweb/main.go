// Command agentweb serves the agent host over the Connect web bridge — the
// browser analogue of the terminal agentchat, and a thin surface over
// agent/host.App exactly as agentchat is (issue 1196). It loads a host config,
// builds one App, and serves the HostService plus the DockView + Solid frontend
// (issue 1197) on one servicekit listener.
//
// `--demo` skips the config and builds an App over an offline, inexhaustible
// demo provider, so `go run ./cmd/agentweb --demo` serves a working, streaming
// surface with no model or MCP server to configure — the run/screenshot target.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	skhttp "github.com/panyam/servicekit/http"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
	agentsurfaces "github.com/panyam/mcpkit/experimental/agent/surfaces"
	web "github.com/panyam/mcpkit/experimental/agent/surfaces/web"
)

func main() {
	addr := flag.String("addr", ":8090", "address to listen on")
	configPath := flag.String("config", "", "path to a host config JSON (servers, model, policies)")
	demo := flag.Bool("demo", false, "run over an offline streaming demo provider (no config needed)")
	sessionStore := flag.String("session-store", "", "session persistence backend: memory | sqlite://path.db | redis://host:port | postgres://user:pass@host:port/db (empty = off, sessions live only in memory)")
	workspace := flag.String("workspace", "", "expose file tools (read/edit/write/list/search) confined to this directory, plus checkpoint /undo (empty = off; there is no default directory on purpose)")
	noCheckpoint := flag.Bool("no-checkpoint", false, "with --workspace, skip the file snapshot taken before each write, disabling /undo and /checkpoints")
	flag.Parse()

	// A configured --session-store makes web sessions durable: session_id is a
	// run id, and a session dropped by a restart rehydrates from the store on
	// the next request. Empty leaves the manager as a pure in-memory cache.
	store, err := agentsurfaces.RunStoreFromSpec(*sessionStore)
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}

	// One factory builds a fresh App per web session from the same config, so a
	// CreateSession over the wire mints an independent conversation. Every App
	// attaches the same store (buildApp), so a session it hosts persists. The
	// SessionManager holds them all; its default session (created at startup)
	// backs an empty session_id, keeping the single-surface flow unchanged.
	factory := func(ctx context.Context) (*host.App, error) {
		return buildApp(*configPath, *demo, store, *workspace, *noCheckpoint)
	}
	var mgr *web.SessionManager
	if store != nil {
		mgr = web.NewSessionManagerWithStore(factory, store)
	} else {
		mgr = web.NewSessionManager(factory)
	}
	defaultApp, err := factory(context.Background())
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	if store != nil {
		// Give the default session a run too, so single-surface use (an empty
		// session_id) also survives a restart. AttachRun is create-or-resume, so
		// a restart with the same store resumes the prior default conversation.
		if err := defaultApp.AttachRun(context.Background(), web.DefaultSessionID); err != nil {
			log.Fatalf("agentweb: attaching default session run: %v", err)
		}
	}
	mgr.SetDefault(defaultApp)
	defer mgr.CloseAll()

	srv := &http.Server{Addr: *addr, Handler: web.HandlerWithSessions(mgr)}
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", *addr, "mcpkit.agentweb.v1.HostService")
	if err := skhttp.ListenAndServeGraceful(srv); err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}

// buildApp builds one App: the factory the SessionManager calls for the default
// session and every CreateSession. In demo mode it wires the offline demo
// provider; otherwise it loads the host config. The web surface renders in the
// browser off the event stream, so the App's own terminal renderer is discarded
// (output to io.Discard) — events reach clients through Subscribe / Watch. A
// non-nil store is attached to every App (host.WithRunStore) so each session,
// including the default, persists its turns — the invariant the store-backed
// SessionManager relies on when it rehydrates a dropped session.
func buildApp(configPath string, demo bool, store agent.RunStore, workspace string, noCheckpoint bool) (*host.App, error) {
	var storeOpt []host.AppOption
	if store != nil {
		storeOpt = append(storeOpt, host.WithRunStore(store))
	}
	// Built once and appended to whichever App this call constructs, so the
	// demo and config paths cannot drift into offering different tools. Nil
	// when --workspace is unset, leaving both paths as they were.
	wsExts, err := agentsurfaces.WorkspaceExtensions(agentsurfaces.WorkspaceConfig{
		Root:         workspace,
		NoCheckpoint: noCheckpoint,
	})
	if err != nil {
		return nil, err
	}
	if len(wsExts) > 0 {
		storeOpt = append(storeOpt, host.WithExtension(wsExts...))
	}
	if demo {
		// Wire two delegate personas + a low offload threshold so a demo turn
		// exercises the whole observability surface: the scripted provider fans
		// out to researcher/analyst (sub-agent tree + tool calls), the
		// researcher's long reply comes back offloaded (tool inspector stub),
		// and reported usage moves the budget gauges.
		cfg := &host.Config{
			Model: host.ModelConfig{BaseURL: "http://demo", Model: "agentweb-demo"},
			SubAgents: []host.SubAgentConfig{
				{Name: "researcher", Description: "gathers background on a topic", Instructions: "You are a research sub-agent."},
				{Name: "analyst", Description: "assesses risks and trade-offs", Instructions: "You are a risk-analysis sub-agent."},
			},
			Offload: &host.OffloadConfig{ThresholdBytes: 300, PreviewLen: 120},
			// Gate the researcher delegate behind an approval "ask" (everything
			// else auto-runs) so a demo turn raises exactly one elicitation. The
			// prompt broadcasts to every surface; answering it on any surface
			// retracts it on the others, which is what this build (issue 1199)
			// exists to demonstrate.
			Approval: &host.ApprovalConfig{Mode: "allow", Rules: map[string]string{"researcher": "ask"}},
		}
		return host.NewApp(cfg, io.Discard, strings.NewReader(""),
			append(storeOpt,
				host.WithProvider(demoProvider{}),
				host.WithElicitationUI(demoElicitUI),
			)...,
		)
	}
	if configPath == "" {
		return nil, fmt.Errorf("--config <path> is required (or pass --demo for the offline streaming demo)")
	}
	cfg, err := host.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), storeOpt...)
}
