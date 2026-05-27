//go:build devtunnel && windows

package main

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr is a no-op on Windows; process groups are handled differently
// and cloudflared on Windows reacts to regular Kill().
func setSysProcAttr(cmd *exec.Cmd) {}

// signalTunnel on Windows cannot deliver POSIX signals; just kill the process.
func signalTunnel(cmd *exec.Cmd, _ syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
