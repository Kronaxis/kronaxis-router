//go:build windows

package runner

import "os/exec"

// configureProcessGroup is a no-op on Windows; CREATE_NEW_PROCESS_GROUP
// would be the equivalent but exec.CommandContext + signal Kill works
// without it on Windows for our use case.
func configureProcessGroup(_ *exec.Cmd) {}

// killProcessGroup falls back to killing the direct child.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
