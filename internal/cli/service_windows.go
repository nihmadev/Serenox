//go:build windows

package cli

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

func executeDaemon(ctx context.Context, binary string, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return &StatusError{Code: exitErr.ExitCode(), Err: err, SuppressReport: true}
		}
		return err
	}
	return nil
}
