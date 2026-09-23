//go:build !windows

// Package diskfree reports free disk space for the filesystem holding a path.
package diskfree

import "syscall"

// Available reports the space available to this user on the filesystem
// holding path.
func Available(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	// Bavail (not Bfree) — the root-reserved blocks are not ours to fill.
	return st.Bavail * uint64(st.Bsize), true
}
