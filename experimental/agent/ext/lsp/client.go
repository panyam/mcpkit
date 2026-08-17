package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// position is an LSP text position. Character is counted in the encoding the
// server agreed to at initialize, which is utf-16 unless it said otherwise.
type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type textRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string    `json:"uri"`
	Range textRange `json:"range"`
}

// diagnostic is one problem the server reports about a file.
//
// Unexported because nothing outside this package consumes it: the extension
// renders diagnostics to text for the model, and a caller wanting structured
// problems wants the language server, not this wrapper around it.
type diagnostic struct {
	Range    textRange `json:"range"`
	Severity int       `json:"severity"`
	Source   string    `json:"source"`
	Message  string    `json:"message"`
}

// severity levels, as the protocol numbers them.
const (
	severityError   = 1
	severityWarning = 2
)

// documentSymbol is one entry of the file outline.
//
// Range covers the whole declaration and SelectionRange covers just the name,
// which is the distinction that makes name-addressed navigation work: a
// request aimed anywhere in a function body is ambiguous, while one aimed at
// the identifier is what an editor would send with the cursor on it.
type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          textRange        `json:"range"`
	SelectionRange textRange        `json:"selectionRange"`
	Children       []documentSymbol `json:"children"`
}

// client drives one language server subprocess.
type client struct {
	spec ServerSpec
	root string

	cmd  *exec.Cmd
	conn *conn

	// encoding is what the server agreed to for Position.Character. Empty
	// means it declined to negotiate, which per spec means utf-16.
	encoding string

	mu      sync.Mutex
	diags   map[string][]diagnostic
	open    map[string]int
	waiters map[string][]chan struct{}

	closeOnce sync.Once
}

// startClient spawns the server and completes the initialize handshake.
//
// The handshake is synchronous on purpose. A server that has not answered
// initialize cannot be sent anything else, and deferring the failure would
// turn "gopls is not installed" into a diagnostics block that is silently
// always empty.
func startClient(ctx context.Context, spec ServerSpec, root string) (*client, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("lsp: ServerSpec.Command is required")
	}
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s stdin: %w", spec.Command[0], err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s stdout: %w", spec.Command[0], err)
	}
	// The server's stderr is its own diagnostics channel, not ours. Discarding
	// it keeps a chatty server from filling a pipe nobody drains, which would
	// block it mid-response and look like a hang.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", spec.Command[0], err)
	}

	c := &client{
		spec:    spec,
		root:    root,
		cmd:     cmd,
		conn:    newConn(stdin, stdout),
		diags:   map[string][]diagnostic{},
		open:    map[string]int{},
		waiters: map[string][]chan struct{}{},
	}
	c.conn.onNotify = c.onNotify
	go c.conn.run()

	if err := c.initialize(ctx); err != nil {
		_ = c.close()
		return nil, err
	}
	return c, nil
}

func (c *client) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   pathToURI(c.root),
		"workspaceFolders": []map[string]any{
			{"uri": pathToURI(c.root), "name": filepath.Base(c.root)},
		},
		"capabilities": map[string]any{
			// utf-8 first, so a server that negotiates gives us byte offsets
			// and the conversion in offsetFor becomes a no-op. gopls declines
			// and we get utf-16, which is why the conversion exists at all.
			"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"definition":         map[string]any{},
				"references":         map[string]any{},
			},
			"workspace": map[string]any{"configuration": true},
		},
	}
	var result struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if err := c.conn.call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("lsp: initialize %s: %w", c.spec.Command[0], err)
	}
	c.encoding = result.Capabilities.PositionEncoding
	return c.conn.notify("initialized", map[string]any{})
}

func (c *client) onNotify(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	rel, ok := c.relFromURI(p.URI)
	if !ok {
		return
	}
	c.mu.Lock()
	c.diags[rel] = p.Diagnostics
	waiters := c.waiters[rel]
	delete(c.waiters, rel)
	c.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// awaitPublish registers interest in the next diagnostics for rel.
//
// Registered before the didChange that will cause them, because the server can
// publish before the notify call returns and a waiter installed afterwards
// would miss the very publication it is waiting for.
func (c *client) awaitPublish(rel string) <-chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	c.waiters[rel] = append(c.waiters[rel], ch)
	c.mu.Unlock()
	return ch
}

// sync tells the server about the current on-disk content of rel, opening the
// document the first time and revising it afterwards.
func (c *client) sync(rel string) error {
	body, err := os.ReadFile(filepath.Join(c.root, rel))
	if err != nil {
		return err
	}
	c.mu.Lock()
	version, isOpen := c.open[rel]
	version++
	c.open[rel] = version
	c.mu.Unlock()

	uri := pathToURI(filepath.Join(c.root, rel))
	if !isOpen {
		return c.conn.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": c.spec.LanguageID,
				"version":    version,
				"text":       string(body),
			},
		})
	}
	// A single change with no range means "this is the whole document". It is
	// the one form every server accepts regardless of the sync kind it
	// declared, which matters because we drive several servers and cannot
	// carry an incremental-diff implementation per server.
	return c.conn.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": string(body)}},
	})
}

// refresh syncs rel and waits for the diagnostics that follow, up to timeout.
//
// A timeout returns false rather than the diagnostics we happen to be holding.
// Stale problems presented as the result of the edit that just ran are worse
// than saying nothing: the model would go fix an error it already fixed.
func (c *client) refresh(ctx context.Context, rel string, timeout time.Duration) bool {
	ch := c.awaitPublish(rel)
	if err := c.sync(rel); err != nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (c *client) diagnostics(rel string) []diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]diagnostic, len(c.diags[rel]))
	copy(out, c.diags[rel])
	return out
}

// tracked reports the files this client has been told about, which is the set
// worth reporting diagnostics for.
func (c *client) tracked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.open))
	for rel := range c.open {
		out = append(out, rel)
	}
	return out
}

// close shuts the server down, politely first and then not.
//
// The graceful path matters more than it looks: gopls writes its module cache
// and telemetry on exit, and a killed server can leave a stale lock that makes
// the next session slower. The kill is the backstop for a server that ignores
// shutdown, because a host that blocks on Close is worse than an orphan.
func (c *client) close() error {
	var err error
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if callErr := c.conn.call(ctx, "shutdown", nil, nil); callErr == nil {
			_ = c.conn.notify("exit", nil)
		}
		_ = c.conn.w.Close()

		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
			err = fmt.Errorf("lsp: %s did not exit, killed", c.spec.Command[0])
		}
	})
	return err
}

// pathToURI renders a filesystem path as a file:// URI.
//
// url.URL rather than string concatenation because a path can contain spaces
// and characters that need escaping, and a malformed URI comes back from a
// server as an empty result rather than as an error.
func pathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

// relFromURI converts a server-supplied URI back to a workspace-relative path,
// reporting false for anything outside the root.
//
// Out-of-root results are normal rather than exceptional: a definition in the
// standard library or a module cache is a real answer to a real question, and
// it is simply not a file this workspace can talk about by relative path.
func (c *client) relFromURI(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	rel, err := filepath.Rel(c.root, filepath.FromSlash(u.Path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
