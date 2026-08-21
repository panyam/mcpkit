package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/agent"
)

// Source serves one tool per allowlisted command over agent.ToolSource.
//
// One tool each rather than a single run_command taking a name, because the
// safety annotations that drive the approval gate are per tool. Collapsed into
// one tool, every command would share one destructiveHint, and the operator
// could not say that `go build` may run unattended while `terraform apply`
// asks. The containment is the same either way: the model picks from a fixed
// set and never composes a command line.
type Source struct {
	roots     []string
	cmds      map[string]*command
	defs      []core.ToolDef
	sandbox   Sandbox
	maxOutput int
	write     []string
	denyRead  []string
}

// command is one resolved allowlist entry.
type command struct {
	spec    CommandSpec
	tool    string
	path    string
	dir     string
	timeout time.Duration
}

// NewSource resolves the allowlist and the sandbox, failing on anything it
// cannot settle now rather than on the first call.
//
// Resolution at construction is the point of several of these checks. Argv[0]
// goes through PATH once, here, so the tool runs the binary that was on PATH
// when the operator configured it and not whatever a later PATH change puts
// in its place.
func NewSource(cfg Config) (*Source, error) {
	if len(cfg.Roots) == 0 {
		return nil, errors.New("exec: Config.Roots needs at least one directory")
	}
	if len(cfg.Commands) == 0 {
		return nil, errors.New("exec: Config.Commands is empty; there is nothing to run")
	}

	var roots []string
	for _, r := range cfg.Roots {
		if r == "" {
			return nil, errors.New("exec: Config.Roots contains an empty path")
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("exec: resolve root %s: %w", r, err)
		}
		// Through symlinks, because containment is compared against these and
		// /tmp is a symlink to /private/tmp on darwin. Comparing an unresolved
		// root against a resolved path would reject a path inside it.
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("exec: resolve root %s: %w", r, err)
		}
		roots = append(roots, real)
	}

	sandbox, err := resolveSandbox(cfg.Sandbox, defaultSandbox)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOutput := cfg.MaxOutput
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutput
	}
	// Nil means the caller did not choose, empty means they chose nothing.
	// Collapsing the two would make "deny every extra write path" impossible.
	writePaths := cfg.WritePaths
	if writePaths == nil {
		writePaths = DefaultWritePaths
	}
	denyRead := cfg.DenyRead
	if denyRead == nil {
		denyRead = DefaultDenyRead
	}

	s := &Source{
		roots:     roots,
		cmds:      make(map[string]*command, len(cfg.Commands)),
		sandbox:   sandbox,
		maxOutput: maxOutput,
		write:     append(append([]string{}, roots...), expandPaths(writePaths)...),
		denyRead:  expandPaths(denyRead),
	}

	for i := range cfg.Commands {
		c, err := s.resolveCommand(cfg.Commands[i], timeout)
		if err != nil {
			return nil, err
		}
		if _, dup := s.cmds[c.tool]; dup {
			return nil, fmt.Errorf("exec: two commands named %q", c.spec.Name)
		}
		s.cmds[c.tool] = c
		s.defs = append(s.defs, toolDef(c))
	}
	return s, nil
}

func (s *Source) resolveCommand(spec CommandSpec, defaultTimeout time.Duration) (*command, error) {
	if !nameRE.MatchString(spec.Name) {
		return nil, fmt.Errorf("exec: command name %q must match %s", spec.Name, nameRE)
	}
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("exec: command %q has no Argv", spec.Name)
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("exec: command %q has no Description; it is the only thing the model has to choose by", spec.Name)
	}
	path, err := osexec.LookPath(spec.Argv[0])
	if err != nil {
		return nil, fmt.Errorf("exec: command %q: %w", spec.Name, err)
	}
	dir := spec.Dir
	if dir == "" {
		dir = s.roots[0]
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.roots[0], dir)
	}
	dir, err = s.confine(dir)
	if err != nil {
		return nil, fmt.Errorf("exec: command %q Dir: %w", spec.Name, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("exec: command %q Dir %s is not a directory", spec.Name, dir)
	}
	if spec.Args != nil {
		if err := spec.Args.validate(); err != nil {
			return nil, fmt.Errorf("exec: command %q: %w", spec.Name, err)
		}
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &command{spec: spec, tool: toolPrefix + spec.Name, path: path, dir: dir, timeout: timeout}, nil
}

// confine resolves a path and checks it lands inside a root.
//
// It resolves symlinks on the longest part of the path that exists, then
// re-attaches the rest. Requiring the whole path to exist would reject the
// build patterns these commands take (`./agent/...` names no file), and
// skipping symlinks entirely would accept a link inside a root pointing out
// of one.
func (s *Source) confine(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.roots[0], p)
	}

	rest := ""
	probe := p
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		rest = filepath.Join(filepath.Base(probe), rest)
		probe = parent
	}

	for _, root := range s.roots {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is outside every workspace root", p)
}

func toolDef(c *command) core.ToolDef {
	props := map[string]any{}
	if c.spec.Args != nil {
		props["args"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"maxItems":    c.spec.Args.Max,
			"description": argsDescription(c.spec.Args),
		}
	}

	// readOnlyHint only when claimed, and destructiveHint only when the
	// command was declared reversible. An absent destructiveHint reads as
	// destructive, which is the default an unclassified command should get.
	annotations := map[string]any{}
	if c.spec.ReadOnly {
		annotations["readOnlyHint"] = true
	}
	if c.spec.Reversible {
		annotations["destructiveHint"] = false
	}
	if len(annotations) == 0 {
		annotations = nil
	}

	return core.ToolDef{
		Name:        c.tool,
		Title:       "Run " + strings.ReplaceAll(c.spec.Name, "_", " "),
		Description: fmt.Sprintf("%s Runs `%s` in %s.", c.spec.Description, strings.Join(c.spec.Argv, " "), c.dir),
		InputSchema: map[string]any{
			"type":       "object",
			"properties": props,
		},
		Annotations: annotations,
	}
}

func argsDescription(p *ArgPolicy) string {
	b := fmt.Sprintf("Up to %d extra arguments, appended to the command. Each must match %s.", p.Max, "`"+p.Match+"`")
	if p.Paths {
		b += " Each must also name a path inside the workspace."
	}
	return b
}

// Tools returns the definitions, in configuration order.
func (s *Source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call runs one allowlisted command.
//
// A name that is not in the allowlist is a dispatch failure and returns an
// error, per the ToolSource contract. Everything the command itself does,
// including exiting non-zero, comes back as a result the model reads.
func (s *Source) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	c, ok := s.cmds[name]
	if !ok {
		return nil, fmt.Errorf("exec: unknown tool %q", name)
	}
	extra, err := s.checkArgs(c, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	return s.run(ctx, c, extra), nil
}

// checkArgs validates the model's trailing arguments against the spec.
func (s *Source) checkArgs(c *command, args map[string]any) ([]string, error) {
	raw, present := args["args"]
	if c.spec.Args == nil {
		if present {
			return nil, fmt.Errorf("%s takes no arguments", c.tool)
		}
		return nil, nil
	}
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: args must be an array of strings", c.tool)
	}
	if len(list) > c.spec.Args.Max {
		return nil, fmt.Errorf("%s takes at most %d arguments, got %d", c.tool, c.spec.Args.Max, len(list))
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		sv, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: args must be an array of strings", c.tool)
		}
		if !c.spec.Args.re.MatchString(sv) {
			return nil, fmt.Errorf("%s: argument %q is not permitted; it must match %s", c.tool, sv, c.spec.Args.Match)
		}
		if c.spec.Args.Paths {
			if _, err := s.confine(filepath.Join(c.dir, sv)); err != nil {
				return nil, fmt.Errorf("%s: argument %q: %w", c.tool, sv, err)
			}
		}
		out = append(out, sv)
	}
	return out, nil
}

// run spawns the command under the sandbox and collects its output.
func (s *Source) run(ctx context.Context, c *command, extra []string) *core.ToolResult {
	argv := append(append([]string{}, c.spec.Argv...), extra...)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := osexec.Command(c.path, argv[1:]...)
	// Args[0] is what the process sees as its own name. Keeping the
	// configured spelling rather than the resolved path matters to tools that
	// switch behaviour on argv[0].
	cmd.Args = argv
	cmd.Dir = c.dir
	setProcessGroup(cmd)

	stdout := newCapBuffer(s.maxOutput)
	stderr := newCapBuffer(s.maxOutput)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	policy := Policy{Write: s.write, DenyRead: s.denyRead, Dir: c.dir, AllowNetwork: c.spec.AllowNetwork}
	if err := s.sandbox.Confine(cmd, policy); err != nil {
		return toolError(fmt.Sprintf("%s: sandbox: %v", c.tool, err))
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return toolError(fmt.Sprintf("%s: %v", c.tool, err))
	}

	// Kill on timeout ourselves rather than through CommandContext, which
	// signals only the process it started. See killProcessGroup.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		killProcessGroup(cmd)
		waitErr = <-done
	}
	elapsed := time.Since(started)

	exit := cmd.ProcessState.ExitCode()
	res := renderRun(c, argv, exit, elapsed, timedOut, stdout, stderr, waitErr)
	return res
}

// renderRun turns a finished command into the result the model reads.
//
// A non-zero exit is not a tool error. The whole point of running a build or a
// test is to learn that it failed, and flagging that as an error would tell
// the model the tool malfunctioned rather than that the code did. A timeout is
// the other way round: the command did not finish, so nothing it printed is a
// verdict.
func renderRun(c *command, argv []string, exit int, elapsed time.Duration, timedOut bool, stdout, stderr *capBuffer, waitErr error) *core.ToolResult {
	var b strings.Builder
	line := strings.Join(argv, " ")
	switch {
	case timedOut:
		fmt.Fprintf(&b, "%s timed out after %s and was killed. Output so far:\n", line, c.timeout)
	default:
		fmt.Fprintf(&b, "%s exited %d after %s.\n", line, exit, elapsed.Round(time.Millisecond))
	}
	if out := stdout.String(); out != "" {
		fmt.Fprintf(&b, "\nstdout:\n%s", out)
	}
	if errOut := stderr.String(); errOut != "" {
		fmt.Fprintf(&b, "\nstderr:\n%s", errOut)
	}
	if !timedOut && exit != 0 && stdout.String() == "" && stderr.String() == "" && waitErr != nil {
		fmt.Fprintf(&b, "\n%v", waitErr)
	}

	return &core.ToolResult{
		IsError: timedOut,
		Content: []core.Content{{Type: "text", Text: b.String()}},
		StructuredContent: map[string]any{
			"command":     line,
			"exit_code":   exit,
			"timed_out":   timedOut,
			"duration_ms": elapsed.Milliseconds(),
			"stdout":      stdout.String(),
			"stderr":      stderr.String(),
			"truncated":   stdout.Truncated() || stderr.Truncated(),
		},
	}
}

// toolError reports a refusal the model can act on, rather than a dispatch
// failure that would abort the turn.
func toolError(msg string) *core.ToolResult {
	return &core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: msg}}}
}

var _ agent.ToolSource = (*Source)(nil)
