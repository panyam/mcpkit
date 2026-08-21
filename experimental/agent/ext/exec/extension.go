package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

// Extension contributes the allowlisted command tools, the prompt that tells a
// model how to read their output, and an approval prompt that shows the
// command rather than its JSON arguments.
type Extension struct {
	host.BaseExtension
	src *Source
}

// New builds the extension. It fails when the allowlist is empty, when a
// command cannot be resolved on PATH, or when no sandbox is available and
// Config.Sandbox does not name one; see Config.
func New(cfg Config) (*Extension, error) {
	src, err := NewSource(cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{src: src}, nil
}

// Name identifies the extension, and is the source id its tools register
// under.
func (e *Extension) Name() string { return "exec" }

// Tools returns one tool per allowlisted command.
func (e *Extension) Tools() (agent.ToolSource, error) { return e.src, nil }

// PromptSections states how to read a command's result.
//
// Two things a model gets wrong without being told. It treats a non-zero exit
// as a broken tool and retries the call instead of reading the failure, and it
// reads a truncated tail as the whole run.
func (e *Extension) PromptSections() []host.PromptSection {
	return []host.PromptSection{host.PromptSectionFunc(func(context.Context) string {
		return `## Running commands

The commands you can run are fixed. Each is its own tool and there is no way to
compose a new one, so if what you need is not there, say so rather than trying
to reach it through the arguments of something that is.

A non-zero exit is a result, not a failure of the tool. Read the output and act
on what it says. Running the same command again unchanged will produce the same
exit code.

Output is capped, and a capped result says how many bytes were dropped from the
middle. When you see that line, the run you are looking at is incomplete: narrow
the command with arguments if it takes them, rather than concluding from a tail.`
	})}
}

// ApprovalRenderers show the command line a user is being asked to approve.
//
// The default renders the call's JSON trimmed to 200 characters, which for
// these tools shows an argument list without the command it attaches to. The
// question "do you want to run this" is not answerable without the argv, the
// directory, and whether anything is confining it.
func (e *Extension) ApprovalRenderers() []host.ApprovalRenderer {
	return []host.ApprovalRenderer{e.renderApproval}
}

func (e *Extension) renderApproval(_ context.Context, info agent.ToolCallInfo) (string, bool) {
	c, ok := e.src.cmds[info.Call.Name]
	if !ok {
		return "", false
	}
	argv := append([]string{}, c.spec.Argv...)
	if raw := info.Call.Args.Raw(); len(raw) > 0 {
		var parsed struct {
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			// Unparseable arguments are exactly when the generic rendering is
			// the honest one: it shows the raw text instead of a command line
			// assembled from a guess.
			return "", false
		}
		argv = append(argv, parsed.Args...)
	}

	confinement := e.src.sandbox.Name()
	if !c.spec.AllowNetwork {
		confinement += ", no network"
	} else {
		confinement += ", network allowed"
	}
	return fmt.Sprintf("Run %s?\n\n  %s\n  in %s\n  confined by %s",
		c.spec.Name, strings.Join(argv, " "), c.dir, confinement), true
}

var _ host.Extension = (*Extension)(nil)
