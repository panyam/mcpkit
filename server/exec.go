package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// ExecConfig configures a ToolExec tool that wraps a CLI binary as an MCP tool.
//
// This is useful for wrapping existing CLI tools (like build systems, linters,
// code generators) as MCP tools without reimplementing their logic. For example,
// a presentation tool could expose its build command:
//
//	srv.Register(server.ToolExec(server.ExecConfig{
//	    Name:    "build_deck",
//	    Command: "slyds",
//	    Args:    []string{"build"},
//	    BuildArgs: func(args json.RawMessage) ([]string, error) {
//	        var p struct{ Deck string `json:"deck"` }
//	        json.Unmarshal(args, &p)
//	        return []string{"--deck", p.Deck}, nil
//	    },
//	}))
type ExecConfig struct {
	// Name is the MCP tool name (required).
	Name string

	// Description is the tool description shown in tools/list.
	Description string

	// Command is the executable path or name (required).
	Command string

	// Args are static arguments prepended to every invocation.
	Args []string

	// Env are additional environment variables in KEY=VALUE format.
	// These are appended to the current process environment.
	Env []string

	// Dir is the working directory for the subprocess.
	// If empty, inherits the current process working directory.
	Dir string

	// Timeout is the maximum execution time per invocation.
	// Zero means no timeout beyond the request context.
	Timeout time.Duration

	// InputSchema is the JSON Schema for the tool's arguments.
	// If nil, defaults to {"type": "object"}.
	InputSchema any

	// BuildArgs transforms the tool request's JSON arguments into CLI args.
	// The returned args are appended after the static Args.
	// If nil, no dynamic arguments are added — only static Args are used.
	BuildArgs func(args json.RawMessage) ([]string, error)
}

// ToolExec creates a Tool that wraps a CLI command execution. The returned
// Tool can be registered via srv.Register(server.ToolExec(cfg)).
//
// On success (exit code 0), returns TextResult with the combined stdout/stderr.
// On failure (non-zero exit), returns ErrorResult with the output and exit code.
// Context cancellation (including Timeout) is propagated to the subprocess.
func ToolExec(cfg ExecConfig) Tool {
	schema := cfg.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}

	return Tool{
		ToolDef: core.ToolDef{
			Name:        cfg.Name,
			Description: cfg.Description,
			InputSchema: schema,
			Timeout:     cfg.Timeout,
		},
		Handler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			// Build the full argument list: static args + dynamic args.
			args := make([]string, len(cfg.Args))
			copy(args, cfg.Args)

			if cfg.BuildArgs != nil {
				dynamic, err := cfg.BuildArgs(req.Arguments)
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("build args: %s", err)), nil
				}
				args = append(args, dynamic...)
			}

			// Apply per-invocation timeout if configured.
			if cfg.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
				defer cancel()
			}

			cmd := exec.CommandContext(ctx, cfg.Command, args...)
			if cfg.Dir != "" {
				cmd.Dir = cfg.Dir
			}
			if len(cfg.Env) > 0 {
				cmd.Env = append(cmd.Environ(), cfg.Env...)
			}

			out, err := cmd.CombinedOutput()
			output := string(out)

			if err != nil {
				msg := output
				if msg == "" {
					msg = err.Error()
				} else {
					msg = fmt.Sprintf("%s\n%s", msg, err)
				}
				return core.ErrorResult(msg), nil
			}

			return core.TextResult(output), nil
		},
	}
}
