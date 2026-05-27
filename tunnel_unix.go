//go:build devtunnel && !windows

package main

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr puts the child in its own process group so we can signal
// the whole group on stop (cloudflared may spawn helper processes).
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalTunnel sends sig to the entire process group of the cloudflared
// child. Falls back to signalling the process directly if the group lookup
// fails.
func signalTunnel(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		return syscall.Kill(-pgid, sig)
	}
	return cmd.Process.Signal(sig)
}
