//go:build windows

package main

import "golang.org/x/sys/windows"

// diskFreeBytes reports the space available to this user on the volume holding
// path. Windows matters most here: Docker Desktop's disk image grows on the
// host volume, so this is what actually runs out during a pull.
func diskFreeBytes(path string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var freeForCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeForCaller, &total, &totalFree); err != nil {
		return 0, false
	}
	return freeForCaller, true
}
