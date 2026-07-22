package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func TestCommandRegistry_RegisterLookupMatch(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&Command{Name: "provider", Run: nil})
	r.Register(&Command{Name: "quit", Aliases: []string{"exit"}})

	if _, ok := r.Lookup("provider"); !ok {
		t.Fatal("Lookup(provider) failed")
	}
	if _, ok := r.Lookup("/exit"); !ok {
		t.Fatal("Lookup by alias with leading slash failed")
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("Lookup(nope) should fail")
	}
	// Match: prefix "p" and "q" (aliases excluded from canonical names)
	if got := r.Match("p"); len(got) != 1 || got[0] != "provider" {
		t.Fatalf("Match(p) = %v", got)
	}
	if got := r.Match("/qu"); len(got) != 1 || got[0] != "quit" {
		t.Fatalf("Match(/qu) = %v", got)
	}
	if names := r.Names(); len(names) != 2 {
		t.Fatalf("Names() = %v, want 2 canonical", names)
	}
}

func newCmdApp(t *testing.T) (*App, *strings.Builder) {
	t.Helper()
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProviderBuilder(stubBuilder(t)), WithRunStore(agent.NewInMemoryRunStore()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app, &out
}

func TestApp_DispatchUnknownCommand(t *testing.T) {
	app, _ := newCmdApp(t)
	if _, err := app.Dispatch(context.Background(), "/bogus"); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("Dispatch(/bogus) err = %v, want ErrUnknownCommand", err)
	}
}

func TestApp_DispatchProviderListAndSwitch(t *testing.T) {
	ctx := context.Background()
	app, _ := newCmdApp(t)

	res, err := app.Dispatch(ctx, "/provider")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != CmdProviders || res.ActiveProvider != "local" || len(res.Providers) != 2 {
		t.Fatalf("/provider = %+v", res)
	}
	res, err = app.Dispatch(ctx, "/provider cloud")
	if err != nil {
		t.Fatal(err)
	}
	if res.ActiveProvider != "cloud" {
		t.Fatalf("/provider cloud active = %q", res.ActiveProvider)
	}
	// a bad connection name is a command error, not a crash
	if _, err := app.Dispatch(ctx, "/provider nope"); err == nil {
		t.Fatal("/provider nope should error")
	}
}

func TestApp_DispatchSessionResumeFork(t *testing.T) {
	ctx := context.Background()
	app, _ := newCmdApp(t)

	// a turn creates the run so /session has something to report
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	sess, err := app.Dispatch(ctx, "/session")
	if err != nil || sess.Kind != CmdSession || sess.RunID == "" {
		t.Fatalf("/session = (%+v, %v)", sess, err)
	}
	fork, err := app.Dispatch(ctx, "/fork")
	if err != nil {
		t.Fatal(err)
	}
	if fork.Kind != CmdSession || fork.RunID == "" || fork.RunID == sess.RunID {
		t.Fatalf("/fork = %+v (should be a new run id)", fork)
	}
	// resume back to the original
	if _, err := app.Dispatch(ctx, "/resume "+sess.RunID); err != nil {
		t.Fatalf("/resume: %v", err)
	}
	if got := app.RunID(); got != sess.RunID {
		t.Fatalf("resume left run at %q, want %q", got, sess.RunID)
	}
}

func TestApp_DispatchToolsAndQuit(t *testing.T) {
	ctx := context.Background()
	app, _ := newCmdApp(t)

	tools, err := app.Dispatch(ctx, "/tools")
	if err != nil || tools.Kind != CmdTools {
		t.Fatalf("/tools = (%+v, %v)", tools, err)
	}
	q, err := app.Dispatch(ctx, "/quit")
	if err != nil || !q.Quit || q.Kind != CmdQuit {
		t.Fatalf("/quit = (%+v, %v)", q, err)
	}
}

// TestApp_ProviderCompleter pins the argument-completion seam the TUI
// palette (issue 987) will drive.
func TestApp_ProviderCompleter(t *testing.T) {
	app, _ := newCmdApp(t)
	cmd, ok := app.Commands().Lookup("provider")
	if !ok || cmd.Complete == nil {
		t.Fatal("provider command has no completer")
	}
	if got := cmd.Complete("cl"); len(got) != 1 || got[0] != "cloud" {
		t.Fatalf("Complete(cl) = %v, want [cloud]", got)
	}
}

// TestApp_REPLDispatchesCommands drives the real REPL loop over scripted
// input to prove commands still work end to end after the migration.
func TestApp_REPLDispatchesCommands(t *testing.T) {
	app, out := newCmdApp(t)
	in := strings.NewReader("/provider\n/provider cloud\n/quit\n")
	if err := app.REPL(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "local") || !strings.Contains(s, "cloud") {
		t.Fatalf("REPL did not render provider list/switch:\n%s", s)
	}
}

func TestApp_DispatchSessionsListAndSwitch(t *testing.T) {
	ctx := context.Background()
	app, _ := newCmdApp(t)

	// two turns then a fork => three persisted runs
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	first := app.RunID()
	forkRes, err := app.Dispatch(ctx, "/fork")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}

	list, err := app.Dispatch(ctx, "/sessions")
	if err != nil || list.Kind != CmdSessions {
		t.Fatalf("/sessions = (%+v, %v)", list, err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("/sessions listed %d runs, want 2", len(list.Sessions))
	}
	if list.RunID != forkRes.RunID {
		t.Fatalf("/sessions active = %q, want the fork %q", list.RunID, forkRes.RunID)
	}
	// switch back to the first via /sessions <id>
	if _, err := app.Dispatch(ctx, "/sessions "+first); err != nil {
		t.Fatalf("/sessions switch: %v", err)
	}
	if app.RunID() != first {
		t.Fatalf("switch left run at %q, want %q", app.RunID(), first)
	}
}

func TestApp_DispatchServersListAliasAndReconnect(t *testing.T) {
	ctx := context.Background()
	app, _ := newCmdApp(t)

	// /servers lists the connected members; the /mcp alias resolves to the same.
	list, err := app.Dispatch(ctx, "/servers")
	if err != nil || list.Kind != CmdServers {
		t.Fatalf("/servers = (%+v, %v)", list, err)
	}
	if len(list.Servers) == 0 {
		t.Fatalf("/servers listed no members, want the configured server")
	}
	id := list.Servers[0].ID
	alias, err := app.Dispatch(ctx, "/mcp")
	if err != nil || alias.Kind != CmdServers || len(alias.Servers) != len(list.Servers) {
		t.Fatalf("/mcp alias = (%+v, %v)", alias, err)
	}

	// reconnect subcommand is a data-only ack; on a ready member it is a no-op
	// (proven at the client layer) but must not error here.
	rc, err := app.Dispatch(ctx, "/servers reconnect "+id)
	if err != nil || rc.Kind != CmdMessage || !strings.Contains(rc.Message, id) {
		t.Fatalf("/servers reconnect = (%+v, %v)", rc, err)
	}

	// tools subcommand scopes to one server; an unknown server is app state.
	tv, err := app.Dispatch(ctx, "/servers tools "+id)
	if err != nil || tv.Kind != CmdServerTools || tv.ServerID != id {
		t.Fatalf("/servers tools = (%+v, %v)", tv, err)
	}
	if len(tv.Tools) == 0 {
		t.Fatalf("/servers tools listed no tools for %q", id)
	}
	miss, err := app.Dispatch(ctx, "/servers tools nope")
	if err != nil || miss.Kind != CmdMessage {
		t.Fatalf("/servers tools nope = (%+v, %v), want a CmdMessage", miss, err)
	}

	// completer offers the subcommands and the member id.
	cmd, ok := app.Commands().Lookup("servers")
	if !ok || cmd.Complete == nil {
		t.Fatal("servers command has no completer")
	}
	if got := cmd.Complete("rec"); len(got) != 1 || got[0] != "reconnect" {
		t.Fatalf("Complete(rec) = %v, want [reconnect]", got)
	}
	if got := cmd.Complete("to"); len(got) != 1 || got[0] != "tools" {
		t.Fatalf("Complete(to) = %v, want [tools]", got)
	}
	if got := cmd.Complete(id[:1]); len(got) == 0 {
		t.Fatalf("Complete(%q) offered no server id", id[:1])
	}
}

type fakeLoginSource struct{ invalidated int }

func (f *fakeLoginSource) Invalidate() { f.invalidated++ }

func TestApp_LoginServer(t *testing.T) {
	app, _ := newCmdApp(t)
	fake := &fakeLoginSource{}
	app.oauthSources["oauth-srv"] = fake

	if !app.canLogin("oauth-srv") || app.canLogin("bare-srv") {
		t.Fatalf("canLogin: oauth-srv=%v bare-srv=%v", app.canLogin("oauth-srv"), app.canLogin("bare-srv"))
	}

	// login on the configured server drops the cached token (Invalidate) then
	// reconnects; the reconnect on an unknown group member is a harmless no-op.
	app.LoginServer("oauth-srv")
	if fake.invalidated != 1 {
		t.Fatalf("LoginServer did not invalidate the token source (count=%d)", fake.invalidated)
	}
	// unknown / non-oauth server is a no-op (no panic, nothing to invalidate)
	app.LoginServer("bare-srv")

	// the /servers login command refuses a server with no interactive auth type
	res, err := app.Dispatch(context.Background(), "/servers login bare-srv")
	if err != nil || res.Kind != CmdMessage || !strings.Contains(res.Message, "no interactive") {
		t.Fatalf("/servers login bare-srv = (%+v, %v)", res, err)
	}
	// and starts login for the configured one
	res, err = app.Dispatch(context.Background(), "/servers login oauth-srv")
	if err != nil || res.Kind != CmdMessage || !strings.Contains(res.Message, "login started") {
		t.Fatalf("/servers login oauth-srv = (%+v, %v)", res, err)
	}
	if fake.invalidated != 2 {
		t.Fatalf("command path did not invalidate (count=%d)", fake.invalidated)
	}
}

func TestApp_SessionsNoStoreErrors(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := app.Sessions(context.Background()); err == nil {
		t.Fatal("Sessions without a store should error")
	}
}
