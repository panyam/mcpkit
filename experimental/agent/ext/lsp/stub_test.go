package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The stub language server runs as a real subprocess, re-executing this test
// binary with LSP_STUB_SCRIPT set.
//
// A stub wired to an in-process pipe would have been less code, but it would
// have skipped exec, argv, working directory, and every part of teardown that
// only exists because there is a process. Close is the seam this PR adds to
// host.Extension, and a test that never spawns anything cannot show a process
// dying. Re-execing the test binary buys the real path with no fixture binary
// to build and no dependency to add.
const stubEnv = "LSP_STUB_SCRIPT"

func TestMain(m *testing.M) {
	if script := os.Getenv(stubEnv); script != "" {
		runStubServer(script)
		return
	}
	os.Exit(m.Run())
}

// stubScript is what a test tells the stub server to be.
type stubScript struct {
	// Encoding is echoed back as the negotiated positionEncoding. Empty means
	// the field is omitted, which is what gopls does and what makes utf-16 the
	// operative encoding.
	Encoding string `json:"encoding,omitempty"`

	Symbols     map[string][]documentSymbol `json:"symbols,omitempty"`
	Definition  []location                  `json:"definition,omitempty"`
	References  []location                  `json:"references,omitempty"`
	Diagnostics map[string][]diagnostic     `json:"diagnostics,omitempty"`

	// PublishDelayMs delays every publishDiagnostics, for testing the wait.
	PublishDelayMs int `json:"publishDelayMs,omitempty"`

	// NoPublish suppresses diagnostics entirely, so a refresh times out.
	NoPublish bool `json:"noPublish,omitempty"`

	// EmptyFirst publishes an empty set immediately and the real one after
	// PublishDelayMs, which is what rust-analyzer does: the first publication
	// after a change is a provisional clear, not an answer.
	EmptyFirst bool `json:"emptyFirst,omitempty"`

	// PublishOnOpenOnly ignores didChange, like clangd given content it
	// already has.
	PublishOnOpenOnly bool `json:"publishOnOpenOnly,omitempty"`

	// IgnoreShutdown makes the server refuse to exit politely, which is what
	// the kill path in client.close exists for.
	IgnoreShutdown bool `json:"ignoreShutdown,omitempty"`
}

// runStubServer speaks just enough LSP to drive this package's tests.
func runStubServer(script string) {
	var s stubScript
	if err := json.Unmarshal([]byte(script), &s); err != nil {
		os.Exit(2)
	}
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	root, _ := os.Getwd()

	// A publish can emit twice and runs on its own goroutine, so frames need
	// serializing or a header and a body interleave into a stream the client
	// cannot resynchronize.
	var writeMu sync.Mutex
	send := func(msg *message) {
		body, err := json.Marshal(msg)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(body))
		out.Write(body)
	}
	emit := func(uri string, diags []diagnostic) {
		params, _ := json.Marshal(map[string]any{"uri": uri, "diagnostics": diags})
		send(&message{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: params})
	}
	publish := func(uri string) {
		rel := stubRel(root, uri)
		diags := s.Diagnostics[rel]
		if diags == nil {
			diags = []diagnostic{}
		}
		if s.EmptyFirst {
			emit(uri, []diagnostic{})
		}
		if s.PublishDelayMs > 0 {
			time.Sleep(time.Duration(s.PublishDelayMs) * time.Millisecond)
		}
		emit(uri, diags)
	}

	for {
		frame, err := readFrame(in)
		if err != nil {
			// Closing stdin is what stops a well-behaved server, so a stub
			// that exits here would never reach the kill path. A wedged
			// server is one that ignores the pipe closing too.
			if s.IgnoreShutdown {
				time.Sleep(time.Hour)
			}
			return
		}
		var msg message
		if json.Unmarshal(frame, &msg) != nil {
			continue
		}
		reply := func(result any) {
			if msg.ID == nil {
				return
			}
			raw, _ := json.Marshal(result)
			send(&message{JSONRPC: "2.0", ID: msg.ID, Result: raw})
		}

		switch msg.Method {
		case "initialize":
			caps := map[string]any{}
			if s.Encoding != "" {
				caps["positionEncoding"] = s.Encoding
			}
			reply(map[string]any{"capabilities": caps})
		case "textDocument/didOpen", "textDocument/didChange":
			if s.NoPublish {
				continue
			}
			if s.PublishOnOpenOnly && msg.Method == "textDocument/didChange" {
				continue
			}
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			go publish(p.TextDocument.URI)
		case "textDocument/documentSymbol":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			syms := s.Symbols[stubRel(root, p.TextDocument.URI)]
			if syms == nil {
				syms = []documentSymbol{}
			}
			reply(syms)
		case "textDocument/definition":
			reply(s.Definition)
		case "textDocument/references":
			reply(s.References)
		case "shutdown":
			if s.IgnoreShutdown {
				continue
			}
			reply(nil)
		case "exit":
			if s.IgnoreShutdown {
				continue
			}
			return
		}
	}
}

func stubRel(root, uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, filepath.FromSlash(u.Path))
	if err != nil {
		return ""
	}
	return rel
}

// stubSpec builds a ServerSpec that re-executes this test binary as the stub.
func stubSpec(t *testing.T, s stubScript) ServerSpec {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	// The child inherits this process's environment, so setting it here is
	// what flips the re-executed binary into server mode.
	t.Setenv(stubEnv, string(raw))
	return ServerSpec{
		Command:    []string{os.Args[0]},
		Extensions: []string{".go"},
		LanguageID: "go",
	}
}

// workspace writes files into a temp dir and returns its path.
func workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// t.TempDir on darwin hands back a /var symlink to /private/var, and the
	// server reports URIs under the resolved path. Comparing the two as text
	// would make every relFromURI in these tests fail for a reason that has
	// nothing to do with the code under test.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
