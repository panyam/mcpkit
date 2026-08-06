package web

import (
	"net/http"

	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/agent/web/gen/go/mcpkit/agentweb/v1/agentwebv1connect"
)

// Handler builds the one mux the web surface serves, mirroring the Agni /
// diffpp serve pattern: the HostService Connect handlers under their proto
// service path, the frontend bundle under /static/, and a server-rendered shell
// catching the rest at /. E3 ships a placeholder shell that names the live
// endpoints; the full DockView + Solid frontend is E4 (issue 1197), a separate
// PR that only swaps the shell and /static assets — the Connect surface here is
// what it consumes.
//
// Connect server-streaming (Watch) rides the Connect protocol, which supports
// server streams over HTTP/1.1, so no h2c is required for local dev; a
// production deployment behind HTTP/2 works unchanged.
func Handler(app *host.App) http.Handler {
	return handlerFor(NewHostService(app))
}

// HandlerWithSessions is Handler for a multi-session server: it serves the same
// mux over a SessionManager, so one server hosts many concurrent conversations
// (empty session_id routes to the manager's default, CreateSession mints more).
func HandlerWithSessions(mgr *SessionManager) http.Handler {
	return handlerFor(NewHostServiceWithSessions(mgr))
}

func handlerFor(svc *HostService) http.Handler {
	mux := http.NewServeMux()

	path, h := agentwebv1connect.NewHostServiceHandler(svc)
	mux.Handle(path, h)

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticAssets()))))
	mux.HandleFunc("/", shellHandler)
	return mux
}
