//go:build !unix

package exec

import osexec "os/exec"

// setProcessGroup is a no-op where there are no process groups. Nothing
// reaches here today: every platform without one also has no sandbox backend,
// so construction refuses first. It exists so the package builds.
func setProcessGroup(*osexec.Cmd) {}

// killProcessGroup kills the process alone. See setProcessGroup.
func killProcessGroup(cmd *osexec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
