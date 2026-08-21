//go:build unix

package exec

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestATimeoutKillsTheWholeProcessTree pins the reason the command runs in its
// own process group.
//
// A command is normally a launcher, and killing the launcher leaves the
// children that were doing the work holding the workspace and the pipes. The
// helper starts a grandchild that sleeps far past the timeout and prints its
// pid, so the test can ask afterwards whether it is still there.
func TestATimeoutKillsTheWholeProcessTree(t *testing.T) {
	spec := CommandSpec{
		Name:        "launcher",
		Argv:        helperArgv(t, "spawn"),
		Description: "Start a child and wait.",
		Timeout:     500 * time.Millisecond,
	}
	src := mustSource(t, baseConfig(t, spec))

	res, err := src.Call(context.Background(), "run_launcher", nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := grandchildPID(t, res.Content[0].Text)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d outlived the timeout; the kill reached only the process we started", pid)
}

func grandchildPID(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 1 {
			return pid
		}
	}
	t.Fatalf("the helper did not report a grandchild pid:\n%s", out)
	return 0
}
