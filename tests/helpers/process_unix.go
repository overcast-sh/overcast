//go:build !windows

package helpers

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this PID exists on this host.
// Signal 0 checks existence and permission without delivering anything: no
// error means it exists, EPERM means it exists but belongs to someone else, and
// only ESRCH means there is no such process.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
