//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package plugin

func testProcessExists(_ int) bool { return false }
