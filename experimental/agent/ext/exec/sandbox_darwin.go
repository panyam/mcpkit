package exec

import (
	"fmt"
	"os"
	osexec "os/exec"
)

// sandboxExecPath is the confinement helper macOS ships. It is deprecated and
// has been for a decade, with no supported replacement for confining a child
// process from userland, so this is the backend that exists rather than the
// one to recommend.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// defaultSandbox picks the darwin backend.
func defaultSandbox() Sandbox { return sandboxExec{} }

type sandboxExec struct{}

func (sandboxExec) Name() string { return "sandbox-exec" }

func (sandboxExec) Available() error {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return fmt.Errorf("%s: %w", sandboxExecPath, err)
	}
	return nil
}

// Confine re-points cmd at sandbox-exec, passing the profile inline.
//
// Inline rather than through a file because a file is state with a lifetime:
// it has to be created, made unreadable to anyone else, and removed on every
// exit path including a kill. The profile is a few kilobytes and argv has room
// for it.
func (sandboxExec) Confine(cmd *osexec.Cmd, p Policy) error {
	profile := buildSBPL(p)
	cmd.Args = append([]string{sandboxExecPath, "-p", profile, "--"}, cmd.Args...)
	cmd.Path = sandboxExecPath
	return nil
}
