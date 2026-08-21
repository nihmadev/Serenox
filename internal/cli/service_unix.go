//go:build !windows

package cli

import (
	"context"
	"io"
	"os"
	"syscall"
)

func executeDaemon(_ context.Context, binary string, arguments []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
	argv := make([]string, 1, len(arguments)+1)
	argv[0] = binary
	argv = append(argv, arguments...)
	return syscall.Exec(binary, argv, os.Environ())
}
