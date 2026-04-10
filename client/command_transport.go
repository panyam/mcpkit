package client

// CommandTransport spawns a subprocess MCP server and communicates with it
// over stdin/stdout using Content-Length framed JSON-RPC. It manages the full
// process lifecycle: start on Connect, graceful shutdown on Close.
//
// Under the hood, CommandTransport wraps StdioTransport for the wire protocol
// and adds process management (startup, SIGTERM/SIGKILL shutdown, stderr
// capture, environment passthrough).
//
// Example:
//
//	transport := client.NewCommandTransport("python", []string{"my_server.py"},
//	    client.WithEnv("DEBUG=1"),
//	    client.WithShutdownTimeout(10*time.Second),
//	)
//	c := client.NewClient("", info, client.WithTransport(transport))
//	err := c.Connect()

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	core "github.com/panyam/mcpkit/core"
)

const (
	defaultShutdownTimeout = 5 * time.Second
	defaultStderrBufSize   = 8192
)

// CommandTransport implements core.Transport by spawning a subprocess and
// communicating via stdio. The subprocess is started on Connect() and
// gracefully shut down on Close().
type CommandTransport struct {
	name string
	args []string
	opts commandOpts

	// Handlers set before Connect(), forwarded to the inner StdioTransport.
	serverReqHandler core.ServerRequestHandler
	notifyHandler    core.NotificationHandler

	// Process state (set during Connect, cleared on Close).
	cmd    *exec.Cmd
	stdio  *StdioTransport
	stderr *bytes.Buffer // captures subprocess stderr output
	mu     sync.Mutex
	done   chan struct{} // closed when process exits
}

// commandOpts holds configuration set via CommandOption functions.
type commandOpts struct {
	env             []string      // additional env vars (KEY=VALUE), appended to os.Environ()
	dir             string        // working directory for the subprocess
	shutdownTimeout time.Duration // time to wait after SIGTERM before SIGKILL (default 5s)
	stderrWriter    io.Writer     // optional additional stderr destination
}

// CommandOption configures a CommandTransport.
type CommandOption func(*commandOpts)

// WithEnv adds environment variables to the subprocess. Each value should be
// in KEY=VALUE format. These are appended to the current process environment.
func WithEnv(env ...string) CommandOption {
	return func(o *commandOpts) { o.env = append(o.env, env...) }
}

// WithDir sets the working directory for the subprocess.
func WithDir(dir string) CommandOption {
	return func(o *commandOpts) { o.dir = dir }
}

// WithShutdownTimeout sets the duration to wait after sending SIGTERM before
// escalating to SIGKILL. Default is 5 seconds.
func WithShutdownTimeout(d time.Duration) CommandOption {
	return func(o *commandOpts) { o.shutdownTimeout = d }
}

// WithStderr sets an additional writer for subprocess stderr output. Stderr is
// always captured in an internal buffer (for error messages on crash); this
// option tees it to an additional destination (e.g., os.Stderr, a logger).
func WithStderr(w io.Writer) CommandOption {
	return func(o *commandOpts) { o.stderrWriter = w }
}

// NewCommandTransport creates a CommandTransport that will spawn the given
// command with args when Connect() is called.
func NewCommandTransport(name string, args []string, opts ...CommandOption) *CommandTransport {
	ct := &CommandTransport{name: name, args: args}
	for _, o := range opts {
		o(&ct.opts)
	}
	if ct.opts.shutdownTimeout <= 0 {
		ct.opts.shutdownTimeout = defaultShutdownTimeout
	}
	return ct
}

// Connect starts the subprocess and establishes the stdio transport.
// The context is used only to bound the connect/handshake phase — the subprocess
// outlives it. If the context expires before the handshake completes, Connect
// returns an error and Close() should be called to clean up the process.
func (ct *CommandTransport) Connect(ctx context.Context) error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Use exec.Command (not CommandContext) so the process isn't killed when
	// the connect timeout context expires. Process lifecycle is managed by
	// Close() via SIGTERM/SIGKILL, not by context cancellation.
	cmd := exec.Command(ct.name, ct.args...)
	if ct.opts.dir != "" {
		cmd.Dir = ct.opts.dir
	}
	if len(ct.opts.env) > 0 {
		cmd.Env = append(cmd.Environ(), ct.opts.env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	// Capture stderr in internal buffer, optionally tee to user writer.
	ct.stderr = bytes.NewBuffer(make([]byte, 0, defaultStderrBufSize))
	if ct.opts.stderrWriter != nil {
		cmd.Stderr = io.MultiWriter(ct.stderr, ct.opts.stderrWriter)
	} else {
		cmd.Stderr = ct.stderr
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", ct.name, err)
	}

	ct.cmd = cmd
	ct.done = make(chan struct{})

	// Monitor process exit in background.
	go func() {
		cmd.Wait()
		close(ct.done)
	}()

	// Create and connect the stdio transport over the process pipes.
	ct.stdio = NewStdioTransport(stdout, stdin)
	ct.stdio.serverReqHandler = ct.serverReqHandler
	ct.stdio.notifyHandler = ct.notifyHandler
	return ct.stdio.Connect(ctx)
}

// Call delegates to the underlying StdioTransport.
func (ct *CommandTransport) Call(ctx context.Context, req *core.Request) (*core.Response, error) {
	ct.mu.Lock()
	stdio := ct.stdio
	ct.mu.Unlock()
	if stdio == nil {
		return nil, fmt.Errorf("command transport not connected")
	}
	return stdio.Call(ctx, req)
}

// Notify delegates to the underlying StdioTransport.
func (ct *CommandTransport) Notify(ctx context.Context, req *core.Request) error {
	ct.mu.Lock()
	stdio := ct.stdio
	ct.mu.Unlock()
	if stdio == nil {
		return fmt.Errorf("command transport not connected")
	}
	return stdio.Notify(ctx, req)
}

// SessionID returns "command" for the command transport.
func (ct *CommandTransport) SessionID() string { return "command" }

// Close gracefully shuts down the subprocess. It first closes the stdin pipe
// (via StdioTransport) so well-behaved servers exit on EOF. If the process
// doesn't exit within a short grace period, it sends SIGTERM. If SIGTERM
// doesn't work within the shutdown timeout, it escalates to SIGKILL.
//
// The grace period before SIGTERM handles servers that don't exit on stdin EOF
// (e.g., started in HTTP mode by mistake). Without this, StdioTransport.Close()
// would block forever waiting for readLoop to see EOF on a stdout pipe held
// open by a still-running process.
func (ct *CommandTransport) Close() error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.cmd == nil {
		return nil
	}

	// Check if process already exited.
	select {
	case <-ct.done:
		if ct.stdio != nil {
			ct.stdio.Close()
			ct.stdio = nil
		}
		return ct.exitError()
	default:
	}

	// Close stdin pipe to signal EOF. Well-behaved stdio servers exit here.
	if ct.stdio != nil {
		// Close the writer (stdin) without waiting for readLoop — readLoop
		// will unblock when the process exits and closes stdout.
		if closer, ok := ct.stdio.w.(io.Closer); ok {
			closer.Close()
		}
	}

	// Short grace period for stdin-EOF-based shutdown.
	select {
	case <-ct.done:
		// Process exited cleanly on stdin EOF.
		if ct.stdio != nil {
			ct.stdio.Close()
			ct.stdio = nil
		}
		return ct.exitError()
	case <-time.After(2 * time.Second):
	}

	// Process didn't exit on stdin EOF — send SIGTERM.
	if ct.cmd.Process != nil {
		ct.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait for SIGTERM or escalate to SIGKILL.
	select {
	case <-ct.done:
	case <-time.After(ct.opts.shutdownTimeout):
		if ct.cmd.Process != nil {
			ct.cmd.Process.Kill()
		}
		<-ct.done
	}

	// Now safe to close stdio — readLoop will see EOF from the dead process.
	if ct.stdio != nil {
		ct.stdio.Close()
		ct.stdio = nil
	}

	return ct.exitError()
}

// Stderr returns the captured stderr output from the subprocess. Safe to call
// after Close(). Returns an empty string if no stderr was captured.
func (ct *CommandTransport) Stderr() string {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.stderr == nil {
		return ""
	}
	return ct.stderr.String()
}

// exitError returns nil if the process exited cleanly (exit code 0), or an
// error including the exit status and last stderr output.
func (ct *CommandTransport) exitError() error {
	if ct.cmd.ProcessState != nil && ct.cmd.ProcessState.Success() {
		return nil
	}
	stderr := ""
	if ct.stderr != nil && ct.stderr.Len() > 0 {
		stderr = ct.stderr.String()
		// Truncate to last 1024 bytes for error message readability.
		if len(stderr) > 1024 {
			stderr = "..." + stderr[len(stderr)-1024:]
		}
	}
	if stderr != "" {
		return fmt.Errorf("process %s exited: %v\nstderr: %s", ct.name, ct.cmd.ProcessState, stderr)
	}
	return fmt.Errorf("process %s exited: %v", ct.name, ct.cmd.ProcessState)
}
