//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package plugin

import (
	"errors"
	"os"
	"os/exec"
)

// Non-Unix ports can replace these functions with a Job Object implementation
// without changing Supervisor. The fallback still guarantees termination of
// the direct child.
func configureProcessGroup(_ *exec.Cmd) {}

func signalProcessTree(command *exec.Cmd, _ bool) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
