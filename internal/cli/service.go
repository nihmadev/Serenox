package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// DaemonHandler returns a service command handler. On Unix it replaces the CLI
// process with serenoxd, which preserves stdin/stdout/stderr, signals, and the
// daemon's exact exit status. Other platforms use an equivalent child process
// adapter.
func DaemonHandler(prefix ...string) CommandHandler {
	forward := append([]string(nil), prefix...)
	return func(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		binary, err := findDaemonBinary()
		if err != nil {
			return err
		}
		combined := make([]string, 0, len(forward)+len(arguments))
		combined = append(combined, forward...)
		combined = append(combined, arguments...)
		return executeDaemon(ctx, binary, combined, stdin, stdout, stderr)
	}
}

func findDaemonBinary() (string, error) {
	name := "serenoxd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("locate %s next to serenox or in PATH: %w", name, err)
	}
	return resolved, nil
}
