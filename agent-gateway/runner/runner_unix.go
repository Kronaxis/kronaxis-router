//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup tells the OS to give the subprocess its own process
// group so we can later SIGKILL the entire tree.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the entire process group of the
// subprocess, nuking the direct child plus any grandchildren.
//
// We pass `-pgid` to syscall.Kill, which is the POSIX convention for
// "send to the process group whose leader has this pid". Returns silently
// on any error (the subprocess may already have exited).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fallback: kill just the direct child.
		_ = cmd.Process.Kill()
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
