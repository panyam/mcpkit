package exec

import (
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperSentinel marks a re-execution of this test binary as the child process
// a test wants to run.
//
// The mode travels in argv rather than in the environment because the child
// inherits the parent's environment, so an env switch would also turn the test
// process running the assertions into a helper.
const helperSentinel = "__mcpkit_exec_helper__"

// helperArgv builds an allowlist entry that runs this test binary in helper
// mode. Re-executing the test binary is the same trick ext/lsp uses for its
// stub server: it needs nothing installed, and it behaves identically on every
// platform CI runs.
func helperArgv(t *testing.T, mode ...string) []string {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{self, "-test.run=TestHelperProcess", "--", helperSentinel}, mode...)
}

// TestHelperProcess is the child. It exits before the testing package prints
// anything, so a test reading the command's output sees only what the mode
// wrote.
func TestHelperProcess(t *testing.T) {
	if !slices.Contains(os.Args, helperSentinel) {
		t.Skip("not running as a helper")
	}
	i := slices.Index(os.Args, helperSentinel)
	args := os.Args[i+1:]
	if len(args) == 0 {
		os.Exit(9)
	}

	switch args[0] {
	case "echo":
		// Joined with a separator so a test can tell one argument containing a
		// space from two arguments.
		fmt.Println(strings.Join(args[1:], "|"))

	case "fail":
		fmt.Fprintln(os.Stderr, "compile error: undefined: x")
		os.Exit(3)

	case "spam":
		n, _ := strconv.Atoi(args[1])
		fmt.Println("FIRST LINE")
		for i := 0; i < n; i++ {
			fmt.Printf("filler %06d\n", i)
		}
		fmt.Println("LAST LINE")

	case "sleep":
		d, _ := time.ParseDuration(args[1])
		time.Sleep(d)

	case "spawn":
		child := osexec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", helperSentinel, "sleep", "60s")
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(60 * time.Second)

	case "dial":
		conn, err := net.DialTimeout("tcp", args[1], 3*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dial failed:", err)
			os.Exit(6)
		}
		conn.Close()
		fmt.Println("connected")

	case "cwd":
		wd, _ := os.Getwd()
		fmt.Println(wd)

	case "touch":
		if err := os.WriteFile(args[1], []byte("x"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}

	case "read":
		b, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
		os.Stdout.Write(b)
	}
	os.Exit(0)
}
