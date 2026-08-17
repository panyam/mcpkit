package lsp

import (
	"context"
	"crypto/sha256"
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

// busyGrace is how much longer refresh waits for a correction while the server
// reports itself busy.
//
// It is added to the settle rather than replacing the deadline. Waiting for a
// busy server to fall quiet does not work: rust-analyzer reports progress
// almost continuously, so "wait while busy" ran to the full timeout on every
// write, measured at eight seconds each. A bounded extension keeps the
// benefit, which is patience with a server that is visibly working, without
// the cost.
const busyGrace = 1500 * time.Millisecond

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
	// means it declined to negotiate, which per spec means utf-16. gopls,
	// typescript-language-server and pyright all decline; rust-analyzer and
	// clangd answer "utf-8".
	encoding string

	// settle is how long to keep waiting for further diagnostics after one
	// arrives, for a server that does not report progress. See refresh.
	settle time.Duration

	mu    sync.Mutex
	diags map[string][]diagnostic
	open  map[string]int

	// sent is the content hash last given to the server; answered is the hash
	// the diagnostics we hold correspond to. They differ while a change is in
	// flight, which is what tells "the server already agrees with us" apart
	// from "the server has not replied yet".
	sent     map[string][32]byte
	answered map[string][32]byte

	// busyTokens are the work-done progress operations the server says are
	// running, and finished counts the ones that have ended. Together they
	// replace guessing: a server that reports progress tells us when it is
	// working and when it has stopped, which is a better answer than any
	// quiet period we could pick for it.
	busyTokens map[string]bool
	begun      int

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
		spec:       spec,
		root:       root,
		cmd:        cmd,
		conn:       newConn(stdin, stdout),
		diags:      map[string][]diagnostic{},
		open:       map[string]int{},
		sent:       map[string][32]byte{},
		answered:   map[string][32]byte{},
		busyTokens: map[string]bool{},
		waiters:    map[string][]chan struct{}{},
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
			// Asking for progress is what lets refresh wait exactly as long as
			// the server is working instead of guessing a quiet period.
			"window": map[string]any{"workDoneProgress": true},
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
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := c.conn.call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("lsp: initialize %s: %w", c.spec.Command[0], err)
	}
	c.encoding = result.Capabilities.PositionEncoding
	c.settle = c.spec.SettleDelay
	if c.settle <= 0 {
		c.settle = DefaultSettleDelay
	}
	return c.conn.notify("initialized", map[string]any{})
}

func (c *client) onNotify(method string, params json.RawMessage) {
	if method == "$/progress" {
		c.trackProgress(params)
		return
	}
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
	c.answered[rel] = c.sent[rel]
	waiters := c.waiters[rel]
	delete(c.waiters, rel)
	c.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// trackProgress follows the server's work-done progress notifications.
//
// Only begin and end matter. A server that reports progress is telling us when
// a recompute starts and stops, which is strictly better information than a
// timer: rust-analyzer brackets its cargo check this way, so waiting for the
// end costs exactly the time the check took and nothing more.
func (c *client) trackProgress(params json.RawMessage) {
	var p struct {
		Token any `json:"token"`
		Value struct {
			Kind string `json:"kind"`
		} `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	token := fmt.Sprint(p.Token)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p.Value.Kind {
	case "begin":
		c.busyTokens[token] = true
		c.begun++
	case "end":
		delete(c.busyTokens, token)
	}
}

// busy reports whether the server says it is currently working.
func (c *client) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.busyTokens) > 0
}

// awaitPublish registers interest in the next diagnostics for rel.
//
// Registered before the didChange that will cause them, because the server can
// publish before the notify call returns and a waiter installed afterwards
// would miss the very publication it is waiting for. Every channel it hands
// out is either closed by a publication or dropped by dropWait; one that is
// neither stays in the map until the next publication for that file.
func (c *client) awaitPublish(rel string) chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	c.waiters[rel] = append(c.waiters[rel], ch)
	c.mu.Unlock()
	return ch
}

// dropWait removes a waiter that gave up, so a run of timeouts cannot grow the
// waiter list without bound.
func (c *client) dropWait(rel string, ch chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.waiters[rel]
	for i, w := range list {
		if w == ch {
			c.waiters[rel] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// sync tells the server about the current on-disk content of rel, opening the
// document the first time and revising it afterwards. It reports whether
// anything was actually sent.
//
// Content that the server already has is not resent. Beyond saving a needless
// recompute on every navigation call, this is load-bearing for refresh: clangd
// does not re-publish for a didChange whose content is identical, so resending
// would leave a caller waiting for a publication that is never coming.
func (c *client) sync(rel string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(c.root, rel))
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(body)

	c.mu.Lock()
	version, isOpen := c.open[rel]
	if isOpen && c.sent[rel] == sum {
		c.mu.Unlock()
		return false, nil
	}
	version++
	c.open[rel] = version
	c.sent[rel] = sum
	c.mu.Unlock()

	uri := pathToURI(filepath.Join(c.root, rel))
	if !isOpen {
		if err := c.conn.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": c.spec.LanguageID,
				"version":    version,
				"text":       string(body),
			},
		}); err != nil {
			return true, err
		}
		return true, c.save(uri, string(body))
	}
	// A single change with no range means "this is the whole document". It is
	// the one form every server accepts regardless of the sync kind it
	// declared, which matters because we drive several servers and cannot
	// carry an incremental-diff implementation per server.
	if err := c.conn.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": string(body)}},
	}); err != nil {
		return true, err
	}
	return true, c.save(uri, string(body))
}

// save tells the server the file is on disk, which for some servers is the
// only thing that triggers a real re-check.
//
// Measured: rust-analyzer publishes NOTHING for a didChange. Its cargo check
// runs on save, and on save it answers in about 0.2s. Without this, the second
// and every later edit to a Rust file was never re-checked, and the first one
// only worked because opening the document happened to trigger a check.
//
// It is also honest rather than a trick. Everything here reads the file from
// disk, so by the time we describe it to the server it genuinely has been
// saved. Servers that do not care ignore the notification.
func (c *client) save(uri, text string) error {
	return c.conn.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"text":         text,
	})
}

// refresh syncs rel and waits for the diagnostics that follow, up to timeout.
//
// # Why it waits for quiet rather than for one publication
//
// The obvious implementation takes the first publication after the change as
// the answer. That is correct for gopls, typescript-language-server and
// pyright, all of which publish once with what they found, and it is wrong for
// a server that computes in two phases.
//
// rust-analyzer publishes an EMPTY set as soon as the file changes and the
// real diagnostics about two seconds later, once cargo has run. Taking the
// first publication there reports a file that does not compile as "no problems
// reported", which is the worst direction for this to be wrong in: the whole
// point of the post-write report is to tell the model it broke something.
//
// So each publication restarts a settle timer and the last set wins. The
// timeout still bounds the whole wait, which is what stops a server that
// publishes continuously from holding a turn open.
//
// A timeout with nothing published returns false rather than the diagnostics
// we happen to be holding. Stale problems presented as the result of the edit
// that just ran are worse than saying nothing: the model would go fix an error
// it already fixed.
func (c *client) refresh(ctx context.Context, rel string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	first := c.awaitPublish(rel)

	changed, err := c.sync(rel)
	if err != nil {
		c.dropWait(rel, first)
		return false
	}
	if !changed && c.hasAnswered(rel) {
		// The server has this exact content and has already reported on it,
		// so nothing further is coming and what we hold is current. Waiting
		// would report a timeout for a file we can already answer about,
		// which is what clangd does when handed content it already has.
		//
		// Both halves are load-bearing. Unchanged content alone is not
		// enough: right after a didOpen the server has the content and has
		// not replied yet, and returning here would report a file as clean
		// before anything had looked at it.
		c.dropWait(rel, first)
		return true
	}

	if !c.waitPublish(ctx, rel, first, time.Until(deadline)) {
		return false
	}
	// Quiet is the moment we stop waiting for a correction. It moves whenever
	// something is published, and further out while the server reports itself
	// busy, so a slow recheck is not cut off and reported as clean.
	quiet := time.Now().Add(c.settle)
	for {
		now := time.Now()
		switch {
		case ctx.Err() != nil, now.After(deadline):
			return true
		case len(c.diagnostics(rel)) > 0:
			// Something real to report beats waiting for more of it. A later
			// publication can only add problems, and the next turn's context
			// stage carries the full current set anyway, so holding the model
			// up for completeness buys nothing it does not already get.
			return true
		}
		if c.busy() {
			quiet = now.Add(c.settle + busyGrace)
		}
		if now.After(quiet) {
			return true
		}
		// Bounded by the quiet deadline rather than slept through in one go,
		// so busy() is re-read on every pass. A server publishes its
		// provisional empty set and only then announces the work, so a single
		// sample taken right after the first publication misses it.
		wait := min(quiet.Sub(now), deadline.Sub(now))
		next := c.awaitPublish(rel)
		if c.waitPublish(ctx, rel, next, wait) {
			quiet = time.Now().Add(c.settle)
		}
	}
}

// hasAnswered reports whether the diagnostics we hold describe the content the
// server currently has.
func (c *client) hasAnswered(rel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	sent, open := c.sent[rel]
	if !open {
		return false
	}
	answered, ok := c.answered[rel]
	return ok && answered == sent
}

// waitPublish blocks until ch fires, the wait elapses, or ctx ends, dropping
// the waiter in the two cases where nothing arrived.
func (c *client) waitPublish(ctx context.Context, rel string, ch chan struct{}, wait time.Duration) bool {
	if wait <= 0 {
		c.dropWait(rel, ch)
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		c.dropWait(rel, ch)
		return false
	case <-ctx.Done():
		c.dropWait(rel, ch)
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
