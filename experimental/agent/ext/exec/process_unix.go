//go:build unix

package exec

import (
	osexec "os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group.
//
// Without it a timeout kills only the process we started. A command is
// normally a launcher (make, go, npm) whose children do the work, and those
// children survive the parent's death, keep the workspace busy, and keep
// writing to pipes nobody reads. The group is what makes the kill reach them.
func setProcessGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the whole group, falling back to the single process
// when the group is not there to kill (the command exited between the timeout
// firing and this call).
func killProcessGroup(cmd *osexec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
