//go:build !windows

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonHandlerPreservesStreamsArgumentsAndExit(t *testing.T) {
	directory := t.TempDir()
	daemon := filepath.Join(directory, "serenoxd")
	script := "#!/bin/sh\nIFS= read -r line\nprintf 'args=%s input=%s\\n' \"$*\" \"$line\"\nprintf 'daemon diagnostic\\n' >&2\nexit 75\n"
	if err := os.WriteFile(daemon, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonHandlerProcess$")
	command.Env = append(os.Environ(), "SERENOX_CLI_DAEMON_HELPER=1", "PATH="+directory)
	command.Stdin = strings.NewReader("hello\n")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 75 {
		t.Fatalf("exit error = %v", err)
	}
	if got := stdout.String(); got != "args=--config fixture.json input=hello\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "daemon diagnostic\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestDaemonHandlerProcess(t *testing.T) {
	if os.Getenv("SERENOX_CLI_DAEMON_HELPER") != "1" {
		return
	}
	os.Exit(Main(
		context.Background(),
		[]string{"run", "--config", "fixture.json"},
		os.Stdin,
		os.Stdout,
		os.Stderr,
		Options{},
	))
}
