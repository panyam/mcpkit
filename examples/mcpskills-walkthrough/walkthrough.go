package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/examples/common"
)

//go:embed all:fixture
var embeddedFixture embed.FS

// inspectReport mirrors the JSON shape cmd/mcpskills/inspect.go emits
// under --json. Kept local so the example pulls no internal CLI
// packages.
type inspectReport struct {
	URL                string         `json:"url"`
	ClientInfo         string         `json:"clientInfo"`
	CapabilityDeclared bool           `json:"capabilityDeclared"`
	IndexSchema        string         `json:"indexSchema,omitempty"`
	Entries            []inspectEntry `json:"entries,omitempty"`
	HasFailures        bool           `json:"hasFailures"`
}

type inspectEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	Digest      string `json:"digest,omitempty"`
	Description string `json:"description,omitempty"`
	Verified    *bool  `json:"verified,omitempty"`
	Error       string `json:"error,omitempty"`
}

func runDemo() {
	demo := demokit.New("mcpskills CLI walkthrough").
		Dir("mcpskills-walkthrough").
		Description("Drives the mcpskills binary end-to-end against a tiny fixture: verify, serve, inspect, pack, unpack, byte-equality diff. Each step shells out to the real CLI so the walkthrough doubles as a CI smoke test for the published binary surface. Run with --non-interactive in CI; run with --tui or --note for a demo.").
		Actors(
			demokit.Actor("CLI", "mcpskills binary (subprocess)"),
			demokit.Actor("Server", "mcpskills serve (background subprocess)"),
		)

	demo.Section("What this walks",
		"SEP-2640's five host-side tasks each map to one mcpskills subcommand. The walkthrough exercises every one against a fixture written to a temp dir at startup.",
		"",
		"| Task                         | Subcommand                  |",
		"| ---------------------------- | --------------------------- |",
		"| Lint a skills directory      | `mcpskills verify <dir>`    |",
		"| Host the directory over MCP  | `mcpskills serve <dir>`     |",
		"| Talk to any spec server      | `mcpskills inspect <url>`   |",
		"| Pack a skill for transport   | `mcpskills pack <skill-dir>`|",
		"| Extract a packed skill       | `mcpskills unpack <archive>`|",
		"",
		"The walkthrough resolves the binary in this order: $MCPSKILLS_BIN, ./bin/mcpskills under the repo root, then a `go build` of cmd/mcpskills into a temp path. The first two paths exist so repeat runs skip the rebuild.",
	)

	var (
		state          = &runState{}
		// optional comparison URL: when set the inspect step runs a
		// second time against this URL. Demonstrates that the same
		// command works against any spec-compliant server.
		upstreamURL    = os.Getenv("MCPSKILLS_INSPECT_UPSTREAM_URL")
	)

	demo.Step("Locate or build the mcpskills binary").
		Note("Discovery order: $MCPSKILLS_BIN, <repo-root>/bin/mcpskills, else `go build` into a temp file. The temp binary is cleaned up at the end.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			bin, source, err := resolveBinary()
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				state.fatal = err
				return nil
			}
			state.bin = bin
			fmt.Printf("    binary: %s\n    source: %s\n", bin, source)
			return nil
		})

	demo.Step("Write the embedded fixture to a temp dir").
		Note("Two skills: git-workflow (single SKILL.md) and pdf-processing (SKILL.md + a supporting file). Self-contained — no sibling-example dependency.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if state.fatal != nil {
				return nil
			}
			tmp, err := os.MkdirTemp("", "mcpskills-walkthrough-*")
			if err != nil {
				state.fatal = err
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			state.tmpDir = tmp
			state.skillsDir = filepath.Join(tmp, "skills")
			if err := writeEmbeddedFixture(state.skillsDir); err != nil {
				state.fatal = err
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    fixture written under %s\n", state.skillsDir)
			fmt.Println(treeListing(state.skillsDir))
			return nil
		})

	demo.Step("mcpskills verify <tmp>/skills").
		Note("Lints the fixture for SEP-2640 compliance: required SKILL.md, frontmatter name matches directory, no nested SKILL.md.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			stdout, stderr, err := runCLI(state.bin, "verify", state.skillsDir, "--color", "never")
			fmt.Print(indent(stdout))
			if err != nil {
				state.fatal = fmt.Errorf("verify: %w", err)
				fmt.Printf("    ERROR: %v\n%s", err, indent(stderr))
			}
			return nil
		})

	demo.Step("mcpskills serve <tmp>/skills --addr 127.0.0.1:<freeport>").
		Arrow("CLI", "Server", "exec.Command(mcpskills serve ...)").
		DashedArrow("Server", "CLI", "ready (listener up on <freeport>)").
		Note("A free port is picked via net.Listen on :0 then closed, so the child mcpskills server gets an unused port rather than colliding with :8080.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			port, err := freePort()
			if err != nil {
				state.fatal = err
				return nil
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			state.serverURL = fmt.Sprintf("http://%s/mcp", addr)

			serverCtx, cancel := context.WithCancel(context.Background())
			state.cancelServer = cancel
			cmd := exec.CommandContext(serverCtx, state.bin, "serve", state.skillsDir, "--addr", addr)
			cmd.Stdout = &state.serverLog
			cmd.Stderr = &state.serverLog
			if err := cmd.Start(); err != nil {
				state.fatal = err
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			state.serverCmd = cmd
			if err := waitListening(addr, 10*time.Second); err != nil {
				state.fatal = fmt.Errorf("server never came up: %w (log:\n%s)", err, state.serverLog.String())
				fmt.Printf("    ERROR: %v\n", state.fatal)
				return nil
			}
			fmt.Printf("    server up on %s\n    serving from %s\n", addr, state.skillsDir)
			return nil
		})

	demo.Step("mcpskills inspect <url> --json (verify every cataloged digest)").
		Arrow("CLI", "Server", "initialize + resources/read skill://index.json + per-entry verify").
		DashedArrow("Server", "CLI", "JSON report with verified flags per entry").
		Note("The --json flag is what makes this useful inside a script. Each entry's `verified` boolean is a SHA-256 check against the index's promised digest.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			report, raw, err := runInspect(state.bin, state.serverURL)
			if err != nil {
				state.fatal = fmt.Errorf("inspect: %w (raw: %s)", err, raw)
				fmt.Printf("    ERROR: %v\n", state.fatal)
				return nil
			}
			fmt.Printf("    capability declared: %t\n", report.CapabilityDeclared)
			fmt.Printf("    %d entries:\n", len(report.Entries))
			for _, e := range report.Entries {
				verified := "(template)"
				if e.Verified != nil {
					if *e.Verified {
						verified = "verified"
					} else {
						verified = "FAILED"
					}
				}
				fmt.Printf("      %-22s [%s] %s\n", e.Name, e.Type, verified)
			}
			if report.HasFailures {
				state.fatal = fmt.Errorf("inspect reported digest failures")
				fmt.Printf("    ERROR: %v\n", state.fatal)
			}
			return nil
		})

	demo.Step("(optional) Repeat inspect against a second URL").
		Note("Run with MCPSKILLS_INSPECT_UPSTREAM_URL pointed at any spec-compliant server (mcpkit, TS SDK reference, PHP SDK, etc.) and the same command works. Skipped when the env var is unset.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			if upstreamURL == "" {
				fmt.Printf("    MCPSKILLS_INSPECT_UPSTREAM_URL unset — skipping\n")
				return nil
			}
			report, raw, err := runInspect(state.bin, upstreamURL)
			if err != nil {
				fmt.Printf("    upstream inspect FAILED: %v\n    raw: %s\n", err, raw)
				return nil
			}
			fmt.Printf("    upstream %s\n    capability declared: %t, entries: %d, failures: %t\n",
				upstreamURL, report.CapabilityDeclared, len(report.Entries), report.HasFailures)
			return nil
		})

	demo.Step("mcpskills pack <tmp>/skills/pdf-processing -o <tmp>/pdf-processing.tar.gz").
		Note("Packs a single skill directory into a SEP-2640 archive. Output mime is application/gzip.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			state.archivePath = filepath.Join(state.tmpDir, "pdf-processing.tar.gz")
			stdout, stderr, err := runCLI(state.bin,
				"pack", filepath.Join(state.skillsDir, "pdf-processing"),
				"-o", state.archivePath,
				"--color", "never",
			)
			fmt.Print(indent(stdout))
			if err != nil {
				state.fatal = fmt.Errorf("pack: %w", err)
				fmt.Printf("    ERROR: %v\n%s", err, indent(stderr))
				return nil
			}
			info, err := os.Stat(state.archivePath)
			if err == nil {
				fmt.Printf("    archive size on disk: %d bytes\n", info.Size())
				if data, err := os.ReadFile(state.archivePath); err == nil {
					sum := sha256.Sum256(data)
					fmt.Printf("    sha256 of archive: %s\n", hex.EncodeToString(sum[:]))
				}
			}
			return nil
		})

	demo.Step("mcpskills unpack <archive> -o <tmp>/unpacked").
		Note("Unpack enforces the four SEP-2640 safety MUSTs (../ rejection, absolute-path rejection, escaping link rejection, size cap). On clean input that's invisible — but it's the same code path that would refuse a malicious archive.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			state.unpackDir = filepath.Join(state.tmpDir, "unpacked")
			stdout, stderr, err := runCLI(state.bin,
				"unpack", state.archivePath,
				"-o", state.unpackDir,
				"--color", "never",
			)
			fmt.Print(indent(stdout))
			if err != nil {
				state.fatal = fmt.Errorf("unpack: %w", err)
				fmt.Printf("    ERROR: %v\n%s", err, indent(stderr))
			}
			return nil
		})

	demo.Step("Diff the unpacked tree against the source").
		Note("Walks both trees and asserts byte equality file-for-file. A mismatch here would mean pack or unpack lost or corrupted content.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if !state.ready() {
				return nil
			}
			src := filepath.Join(state.skillsDir, "pdf-processing")
			if err := diffTrees(src, state.unpackDir); err != nil {
				state.fatal = fmt.Errorf("round-trip mismatch: %w", err)
				fmt.Printf("    FAILED: %v\n", state.fatal)
				return nil
			}
			fmt.Printf("    round-trip byte-identical (%d files)\n", countFiles(src))
			return nil
		})

	demo.Section("Wrap-up",
		"The walkthrough has exercised every mcpskills subcommand against a fresh fixture. The same shell-out approach a CI smoke test would use is identical to what runs in front of an audience under --tui — the binary doesn't know it's being demoed.",
		"",
		"`just test-mcpskills-walkthrough` at the repo root runs this same flow with --non-interactive and asserts exit 0. Use it as the per-commit gate for the CLI surface.",
	)

	common.SetupRenderer(demo)
	demo.Execute()

	// Teardown after demokit returns so interactive viewers can
	// inspect the temp tree until they hit a key on the final pause.
	if state.cancelServer != nil {
		state.cancelServer()
		_ = state.serverCmd.Wait()
	}
	if state.tmpDir != "" {
		_ = os.RemoveAll(state.tmpDir)
	}
	if state.builtBin != "" {
		_ = os.Remove(state.builtBin)
	}
	if state.fatal != nil {
		fmt.Fprintf(os.Stderr, "walkthrough failed: %v\n", state.fatal)
		os.Exit(1)
	}
}

// runState carries the small bit of state that propagates between
// demokit steps. Demokit doesn't have a typed value channel between
// steps, so a struct closed over by the Run callbacks is the
// convention used by the other examples too.
type runState struct {
	bin          string
	builtBin     string
	tmpDir       string
	skillsDir    string
	archivePath  string
	unpackDir    string
	serverURL    string
	serverCmd    *exec.Cmd
	cancelServer context.CancelFunc
	serverLog    bytes.Buffer
	fatal        error
}

// ready reports whether the previous steps left enough state for the
// next CLI invocation to be meaningful. Once fatal is set, subsequent
// steps no-op so the demo prints clean output up to the failure.
func (s *runState) ready() bool { return s.fatal == nil && s.bin != "" }

// resolveBinary returns the path to a usable mcpskills binary plus a
// human-readable source tag for the walkthrough output.
func resolveBinary() (path, source string, err error) {
	if env := os.Getenv("MCPSKILLS_BIN"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, "$MCPSKILLS_BIN", nil
		}
	}
	if root, err := findRepoRoot(); err == nil {
		candidate := filepath.Join(root, "bin", "mcpskills")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, "<repo-root>/bin/mcpskills", nil
		}
	}
	root, err := findRepoRoot()
	if err != nil {
		return "", "", fmt.Errorf("find repo root: %w", err)
	}
	tmp, err := os.CreateTemp("", "mcpskills-*")
	if err != nil {
		return "", "", err
	}
	tmp.Close()
	cmd := exec.Command("go", "build", "-o", tmp.Name(), "./cmd/mcpskills")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp.Name())
		return "", "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return tmp.Name(), "freshly built from ./cmd/mcpskills", nil
}

// findRepoRoot walks up from the example's directory until it finds
// the root mcpkit go.mod. The walkthrough lives at depth 2
// (examples/mcpskills-walkthrough/) but we don't hard-code that so a
// `go run` from any cwd works.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			if bytes.Contains(data, []byte("module github.com/panyam/mcpkit\n")) ||
				bytes.HasPrefix(data, []byte("module github.com/panyam/mcpkit\n")) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repo root (module github.com/panyam/mcpkit) not found")
		}
		dir = parent
	}
}

// writeEmbeddedFixture copies the embed.FS-baked fixture tree into
// dest. Directory permissions match what `mcpskills serve` and
// `mcpskills verify` expect.
func writeEmbeddedFixture(dest string) error {
	return fs.WalkDir(embeddedFixture, "fixture", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(p, "fixture")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return os.MkdirAll(dest, 0o755)
		}
		outPath := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(outPath, 0o755)
		}
		data, err := embeddedFixture.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(outPath, data, 0o644)
	})
}

// runCLI executes one mcpskills invocation and returns its captured
// stdout and stderr as strings.
func runCLI(bin string, args ...string) (string, string, error) {
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// runInspect runs `mcpskills inspect <url> --json` and decodes the
// emitted report. The raw return value carries the unparsed bytes for
// error messages.
func runInspect(bin, url string) (*inspectReport, string, error) {
	stdout, stderr, runErr := runCLI(bin, "inspect", url, "--json", "--color", "never")
	if runErr != nil && stdout == "" {
		return nil, stderr, runErr
	}
	var report inspectReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, stdout, err
	}
	if runErr != nil && !report.HasFailures {
		// inspect returns non-zero on digest failures but we surfaced
		// those via HasFailures already.
		return &report, stdout, runErr
	}
	return &report, stdout, nil
}

// freePort asks the kernel for an unused TCP port by binding to :0
// and immediately releasing it. A racy approach in the abstract, but
// fine here because the next caller (`mcpskills serve`) binds right
// away and is the only intended user of the port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// waitListening polls a TCP address until a connection succeeds. Used
// to detect the bg server's readiness before the inspect step fires.
func waitListening(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout dialing %s", addr)
}

// diffTrees asserts every file under a is byte-identical to the
// matching path under b. Directory existence is implied by the file
// walk.
func diffTrees(a, b string) error {
	return filepath.WalkDir(a, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(a, path)
		if err != nil {
			return err
		}
		aBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		bBytes, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(aBytes, bBytes) {
			return fmt.Errorf("%s differs", rel)
		}
		return nil
	})
}

func countFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func treeListing(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			b.WriteString("    " + filepath.Base(root) + "/\n")
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		prefix := strings.Repeat("  ", depth)
		suffix := ""
		if d.IsDir() {
			suffix = "/"
		}
		b.WriteString("    " + prefix + filepath.Base(rel) + suffix + "\n")
		return nil
	})
	return b.String()
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("    ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
