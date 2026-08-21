//go:build exec_live && darwin

package exec

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the real sandbox-exec, and are behind a tag because CI runs on
// Linux where this backend does not exist. Nothing in CI exercises a real
// sandbox, so this file is the only thing that checks the profile means what
// the golden test says it says.
//
//	go test -tags exec_live ./...

func liveSource(t *testing.T, root string, spec CommandSpec) *Source {
	t.Helper()
	src, err := NewSource(Config{Roots: []string{root}, Commands: []CommandSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if src.sandbox.Name() != "sandbox-exec" {
		t.Fatalf("expected the real backend, got %s", src.sandbox.Name())
	}
	return src
}

func TestLiveWriteInsideARootSucceeds(t *testing.T) {
	root := workspace(t)
	target := filepath.Join(root, "out.txt")
	src := liveSource(t, root, CommandSpec{
		Name: "touch", Argv: helperArgv(t, "touch", target), Description: "Write a file.",
	})
	if got := call(t, src, "run_touch", nil); !strings.Contains(got, "exited 0") {
		t.Fatalf("a write inside the root must be permitted: %q", got)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
}

// TestLiveWriteOutsideEveryRootIsDenied pins the confinement with the cache
// allowances turned off.
//
// WritePaths is explicitly empty here, and that is not a convenience. The
// default list makes the temp directory writable, because a toolchain cannot
// build without it, so "outside every root" and "denied" are not the same
// statement under the shipped defaults. This test asserts the roots-only
// property; the one below asserts what the defaults actually widen it to.
func TestLiveWriteOutsideEveryRootIsDenied(t *testing.T) {
	root := workspace(t)
	outside := filepath.Join(workspace(t), "escaped.txt")
	src, err := NewSource(Config{
		Roots:      []string{root},
		WritePaths: []string{},
		Commands:   []CommandSpec{{Name: "escape", Argv: helperArgv(t, "touch", outside), Description: "Write outside."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, src, "run_escape", nil)
	if strings.Contains(got, "exited 0") {
		t.Fatalf("the sandbox let a command write outside every root: %q", got)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the file exists; the profile did not confine writes")
	}
}

// TestLiveDefaultWritePathsWidenConfinementToTheCaches records the cost of the
// defaults rather than leaving it to be discovered. A command can write
// anywhere in the temp directory and the build caches, which is what makes a
// real toolchain work and is also the surface an operator is accepting.
func TestLiveDefaultWritePathsWidenConfinementToTheCaches(t *testing.T) {
	root := workspace(t)
	inTemp := filepath.Join(os.TempDir(), "mcpkit-exec-live-probe.txt")
	t.Cleanup(func() { os.Remove(inTemp) })

	src := liveSource(t, root, CommandSpec{
		Name: "temp", Argv: helperArgv(t, "touch", inTemp), Description: "Write to temp.",
	})
	if got := call(t, src, "run_temp", nil); !strings.Contains(got, "exited 0") {
		t.Fatalf("the default write paths cover the temp directory; a build cannot run without it: %q", got)
	}
}

func TestLiveDeniedReadIsDenied(t *testing.T) {
	root := workspace(t)
	home := t.TempDir()
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := NewSource(Config{
		Roots:    []string{root},
		DenyRead: []string{filepath.Join(home, ".ssh")},
		Commands: []CommandSpec{{Name: "steal", Argv: helperArgv(t, "read", secret), Description: "Read a key."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, src, "run_steal", nil)
	if strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("the profile allowed a read it denies: %q", got)
	}
}

func TestLiveLoopbackStaysOpen(t *testing.T) {
	root := workspace(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	src := liveSource(t, root, CommandSpec{
		Name: "dial", Argv: helperArgv(t, "dial", ln.Addr().String()), Description: "Dial loopback.",
	})
	if got := call(t, src, "run_dial", nil); !strings.Contains(got, "connected") {
		t.Fatalf("a test suite that starts a server on 127.0.0.1 is the ordinary case and must work: %q", got)
	}
}

// TestLiveEgressIsDenied is the one assertion here that an offline machine
// cannot distinguish from a pass, since a denied dial and an unreachable
// network both fail. The AllowNetwork half is what separates them, and it is
// skipped rather than failed when the machine has no route out.
func TestLiveEgressIsDenied(t *testing.T) {
	root := workspace(t)
	const target = "1.1.1.1:80"

	closed := liveSource(t, root, CommandSpec{
		Name: "egress", Argv: helperArgv(t, "dial", target), Description: "Dial out.",
	})
	if got := call(t, closed, "run_egress", nil); strings.Contains(got, "connected") {
		t.Fatalf("the profile allowed egress with AllowNetwork off: %q", got)
	}

	open := liveSource(t, root, CommandSpec{
		Name: "egress", Argv: helperArgv(t, "dial", target), Description: "Dial out.", AllowNetwork: true,
	})
	if got := call(t, open, "run_egress", nil); !strings.Contains(got, "connected") {
		t.Skipf("no route to %s, so the denial above proves nothing on this machine: %q", target, got)
	}
}

// TestLiveARealToolchainRunsUnderTheProfile is the test that would have caught
// a write path missing from DefaultWritePaths. A sandboxed `go build` fails on
// its build cache long before it compiles anything.
func TestLiveARealToolchainRunsUnderTheProfile(t *testing.T) {
	root := workspace(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module sandboxprobe\n\ngo 1.21\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	src := liveSource(t, root, CommandSpec{
		Name:        "build",
		Argv:        []string{"go", "build", "./..."},
		Description: "Build the module.",
		Timeout:     2 * time.Minute,
	})
	got := call(t, src, "run_build", nil)
	if !strings.Contains(got, "exited 0") {
		t.Fatalf("a real toolchain could not run under the profile:\n%s", got)
	}
}

var _ = context.Background
