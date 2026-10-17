//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package plugin

import (
	"errors"
	"syscall"
)

func testProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
