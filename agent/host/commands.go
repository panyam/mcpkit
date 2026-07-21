package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// sessionsResult builds the CmdSessions result for the /sessions picker: the
// runs on this page, the active run, and a footer note.
func sessionsResult(a *App, runs []agent.RunInfo, note string) CmdResult {
	return CmdResult{Kind: CmdSessions, Sessions: runs, RunID: a.RunID(), SessionsNote: note}
}

// sessionsFooter is the hint under a page of sessions: how many showed and how
// to page / search / resume.
func sessionsFooter(shown int, hasMore bool, _ string) string {
	f := fmt.Sprintf("%d shown", shown)
	if hasMore {
		f += " · /sessions more for older"
	}
	return f + " · /sessions find <text> to search · /sessions <id> to resume"
}

// CmdKind tags a CmdResult so a surface renders it without re-parsing.
// One kind per distinct command output shape.
type CmdKind string

const (
	// CmdMessage is a plain informational or confirmation line (Message).
	CmdMessage CmdKind = "message"
	// CmdProviders lists model connections (Providers + ActiveProvider).
	CmdProviders CmdKind = "providers"
	// CmdSession reports the active run id (RunID; empty = persistence off).
	CmdSession CmdKind = "session"
	// CmdSessions lists persisted sessions (Sessions + RunID = active).
	CmdSessions CmdKind = "sessions"
	// CmdTools lists the merged tool set (Tools).
	CmdTools CmdKind = "tools"
	// CmdHistory dumps the conversation so far (Messages).
	CmdHistory CmdKind = "history"
	// CmdHealth reports provider failover state (Failover; nil = no backup).
	CmdHealth CmdKind = "health"
	// CmdTasks lists background tasks (Tasks).
	CmdTasks CmdKind = "tasks"
	// CmdApproval reports the approval policy (Approval; nil = gate off).
	CmdApproval CmdKind = "approval"
	// CmdQuit signals the loop to exit (Quit true).
	CmdQuit CmdKind = "quit"
)

// CmdResult is the structured outcome of a command, rendered by the
// surface (terminal today, a web client later) rather than printed by the
// command. Only the fields for the result's Kind are set. Keeping command
// output as data — not io.Writer prints — is what lets every surface reuse
// one command set (the web-version unlock; see the render-seam follow-up
// issue 992, which folds these into the unified HostEvent stream).
type CmdResult struct {
	Kind    CmdKind
	Message string

	Providers      []string
	ActiveProvider string
	RunID          string
	Tools          []core.ToolDef
	Messages       []agent.Message
	Failover       *agent.FailoverProvider
	Tasks          []*client.BackgroundTask
	Approval       *agent.TieredApproval
	Sessions       []agent.RunInfo
	SessionsNote   string
	Quit           bool
}

// Command is one slash command. Run receives the raw argument string
// (everything after the command word, trimmed) and returns a structured
// result. Complete, when set, offers argument completions for a prefix —
// the seam the TUI tab-palette (issue 987) drives; nil means no argument
// completion.
type Command struct {
	Name     string
	Aliases  []string
	Help     string
	Run      func(ctx context.Context, args string) (CmdResult, error)
	Complete func(prefix string) []string
}

// CommandRegistry holds the slash commands a surface dispatches. It is the
// single source of the command set: the REPL, the future TUI palette, and
// a web dispatcher all read from one registry, so a new command registered
// once appears on every surface.
type CommandRegistry struct {
	cmds  map[string]*Command // keyed by name and every alias
	names []string            // canonical names, registration order
}

// NewCommandRegistry returns an empty registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{cmds: map[string]*Command{}}
}

// Register adds c under its name and aliases. A duplicate name/alias
// overwrites — last registration wins — so a surface can shadow a default.
func (r *CommandRegistry) Register(c *Command) {
	if _, seen := r.cmds[c.Name]; !seen {
		r.names = append(r.names, c.Name)
	}
	r.cmds[c.Name] = c
	for _, a := range c.Aliases {
		r.cmds[a] = c
	}
}

// Lookup resolves a command by name or alias (the leading "/" stripped).
func (r *CommandRegistry) Lookup(name string) (*Command, bool) {
	c, ok := r.cmds[strings.TrimPrefix(name, "/")]
	return c, ok
}

// Names returns the canonical command names in registration order.
func (r *CommandRegistry) Names() []string {
	return append([]string(nil), r.names...)
}

// Match returns the canonical names beginning with prefix (the "/" is
// optional), sorted — the command-name completion the TUI palette uses.
func (r *CommandRegistry) Match(prefix string) []string {
	prefix = strings.TrimPrefix(prefix, "/")
	var out []string
	for _, n := range r.names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Dispatch parses a "/name args" line, looks the command up, and runs it.
// A line that is not a known command returns (CmdResult{}, ErrUnknownCommand)
// so the caller can distinguish "not a command, treat as a turn" from a
// command that failed.
func (a *App) Dispatch(ctx context.Context, line string) (CmdResult, error) {
	line = strings.TrimSpace(line)
	word, args, _ := strings.Cut(line, " ")
	cmd, ok := a.commands.Lookup(word)
	if !ok {
		return CmdResult{}, ErrUnknownCommand
	}
	return cmd.Run(ctx, strings.TrimSpace(args))
}

// Commands exposes the registry so a surface can list commands or drive
// tab-completion.
func (a *App) Commands() *CommandRegistry { return a.commands }

// ErrUnknownCommand is returned by Dispatch when the line is not a
// registered command (so the caller treats it as a conversational turn,
// not an error to surface).
var ErrUnknownCommand = errUnknownCommand{}

type errUnknownCommand struct{}

func (errUnknownCommand) Error() string { return "host: unknown command" }

// registerBuiltinCommands populates the registry with the standard slash
// commands, each binding an App method and returning a structured result.
// Surface-agnostic: no rendering happens here.
func (a *App) registerBuiltinCommands() {
	r := NewCommandRegistry()
	msg := func(s string) (CmdResult, error) { return CmdResult{Kind: CmdMessage, Message: s}, nil }

	r.Register(&Command{Name: "quit", Aliases: []string{"exit"}, Help: "exit agentchat",
		Run: func(context.Context, string) (CmdResult, error) { return CmdResult{Kind: CmdQuit, Quit: true}, nil }})

	r.Register(&Command{Name: "tools", Help: "list the merged tool set",
		Run: func(ctx context.Context, _ string) (CmdResult, error) {
			defs, err := a.sources.Tools(ctx)
			if err != nil {
				return CmdResult{}, err
			}
			return CmdResult{Kind: CmdTools, Tools: defs}, nil
		}})

	r.Register(&Command{Name: "history", Help: "show the conversation so far",
		Run: func(context.Context, string) (CmdResult, error) {
			return CmdResult{Kind: CmdHistory, Messages: a.history}, nil
		}})

	r.Register(&Command{Name: "memory", Help: "show working memory (the model's remember/recall scratchpad)",
		Run: func(ctx context.Context, _ string) (CmdResult, error) {
			if a.memory == nil {
				return msg("working memory is off (enable with Config.Memory)")
			}
			summary, err := a.memory.Summary(ctx, agent.SummaryOptions{})
			if err != nil {
				return CmdResult{}, err
			}
			if summary == "" {
				return msg("working memory is empty")
			}
			return msg(summary)
		}})

	r.Register(&Command{Name: "health", Help: "show model failover state",
		Run: func(context.Context, string) (CmdResult, error) {
			return CmdResult{Kind: CmdHealth, Failover: a.failover}, nil
		}})

	r.Register(&Command{Name: "tasks", Help: "list background tasks; /tasks cancel <id>",
		Run: func(_ context.Context, args string) (CmdResult, error) {
			if id, ok := strings.CutPrefix(args, "cancel "); ok {
				a.cancelTask(strings.TrimSpace(id))
				return msg("cancel requested for " + strings.TrimSpace(id))
			}
			return CmdResult{Kind: CmdTasks, Tasks: a.snapshotTasks()}, nil
		}})

	r.Register(&Command{Name: "approve", Help: "show or set approval mode (allow | read-only-auto | ask)",
		Run: func(_ context.Context, args string) (CmdResult, error) {
			if args != "" && a.approval != nil {
				a.approval.SetDefaultMode(parseApprovalMode(args))
			}
			return CmdResult{Kind: CmdApproval, Approval: a.approval}, nil
		}})

	r.Register(&Command{Name: "session", Help: "show the active session id",
		Run: func(context.Context, string) (CmdResult, error) {
			return CmdResult{Kind: CmdSession, RunID: a.RunID()}, nil
		}})

	r.Register(&Command{Name: "sessions", Help: "list recent sessions; 'more' pages, 'find <text>' searches, '<id>' resumes",
		Run: func(ctx context.Context, args string) (CmdResult, error) {
			args = strings.TrimSpace(args)
			switch {
			case args == "":
				page, err := a.SessionsPage(ctx, "")
				if err != nil {
					return CmdResult{}, err
				}
				return sessionsResult(a, page.Runs, sessionsFooter(len(page.Runs), page.HasMore, "")), nil
			case args == "more":
				page, err := a.PageMore(ctx)
				if err != nil {
					return CmdResult{}, err
				}
				if len(page.Runs) == 0 {
					return CmdResult{Kind: CmdMessage, Message: "no more sessions"}, nil
				}
				return sessionsResult(a, page.Runs, sessionsFooter(len(page.Runs), page.HasMore, "")), nil
			case strings.HasPrefix(args, "find "):
				q := strings.TrimSpace(strings.TrimPrefix(args, "find "))
				infos, err := a.SearchSessions(ctx, q)
				if err != nil {
					return CmdResult{}, err
				}
				return sessionsResult(a, infos, fmt.Sprintf("%d match(es) for %q", len(infos), q)), nil
			default:
				// resume by id
				if err := a.Resume(ctx, args); err != nil {
					return CmdResult{}, err
				}
				return CmdResult{Kind: CmdSession, RunID: args}, nil
			}
		},
		Complete: func(prefix string) []string {
			var out []string
			for _, sub := range []string{"more", "find"} {
				if strings.HasPrefix(sub, prefix) {
					out = append(out, sub)
				}
			}
			infos, err := a.Sessions(context.Background())
			if err != nil {
				return out
			}
			for _, r := range infos {
				if strings.HasPrefix(r.ID, prefix) {
					out = append(out, r.ID)
				}
			}
			return out
		}})

	r.Register(&Command{Name: "resume", Help: "switch to an existing session: /resume <id>",
		Run: func(ctx context.Context, args string) (CmdResult, error) {
			if err := a.Resume(ctx, args); err != nil {
				return CmdResult{}, err
			}
			return CmdResult{Kind: CmdSession, RunID: args}, nil
		}})

	r.Register(&Command{Name: "fork", Help: "fork the current session: /fork [new-id]",
		Run: func(ctx context.Context, args string) (CmdResult, error) {
			id, err := a.Fork(ctx, args, 0)
			if err != nil {
				return CmdResult{}, err
			}
			return CmdResult{Kind: CmdSession, RunID: id}, nil
		}})

	r.Register(&Command{Name: "provider", Help: "list or switch model connections: /provider [name]",
		Run: func(_ context.Context, args string) (CmdResult, error) {
			if args != "" {
				if err := a.SwitchProvider(args); err != nil {
					return CmdResult{}, err
				}
			}
			names, active := a.Providers()
			return CmdResult{Kind: CmdProviders, Providers: names, ActiveProvider: active}, nil
		},
		Complete: func(prefix string) []string {
			names, _ := a.Providers()
			var out []string
			for _, n := range names {
				if strings.HasPrefix(n, prefix) {
					out = append(out, n)
				}
			}
			return out
		}})

	a.commands = r
}
