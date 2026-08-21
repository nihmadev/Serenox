// Package cli implements the canonical Serenox command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nihmadev/Serenox/internal/cli/plugincommands"
)

const defaultVersion = "0.1.0-dev"

const usageText = `Usage: serenox <command> [options]

Commands:
  run              run the Serenox service in the foreground
  daemon           alias for run
  login            authorize a Telegram account
  plugin <command> build, verify, install, and manage .srnx plugins
  config check     validate the effective configuration
  version          print the Serenox version

Examples:
  serenox login
  serenox run --config config.json
  serenox plugin install plugin.srnx

Run serenox <command> --help for command options.
`

// CommandHandler is an injectable command implementation. Login is kept as a
// first-class hook so the native Telegram authorization flow can be wired into
// the CLI without a subprocess or a dependency from the CLI to Telegram code.
type CommandHandler func(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error

// Options supplies command hooks. Nil Run and ConfigCheck handlers execute the
// sibling serenoxd service binary. A nil Login handler fails explicitly; the
// canonical cmd/serenox binary always supplies the native authorization hook.
type Options struct {
	Version     string
	Run         CommandHandler
	Login       CommandHandler
	ConfigCheck CommandHandler
}

// ErrLoginNotConfigured is returned when an alternate CLI embedder omits login.
var ErrLoginNotConfigured = errors.New("Telegram login is not connected to this build; install a build with the native authorization handler")

// Main executes the CLI, reports a command error once, and returns the process
// exit code. Callers should pass the returned value to os.Exit.
func Main(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, options Options) int {
	err := Execute(ctx, arguments, stdin, stdout, stderr, options)
	if err == nil {
		return 0
	}
	var status *StatusError
	if !errors.As(err, &status) || !status.SuppressReport {
		fmt.Fprintln(stderr, "error:", err)
	}
	return ExitCode(err)
}

// Execute dispatches one canonical CLI command.
func Execute(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, options Options) error {
	if len(arguments) == 0 {
		_, _ = io.WriteString(stderr, usageText)
		return &StatusError{Code: 2, Err: errors.New("a command is required")}
	}
	version := options.Version
	if version == "" {
		version = defaultVersion
	}
	switch arguments[0] {
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, usageText)
		return nil
	case "version", "-version", "--version":
		if len(arguments) != 1 {
			return &StatusError{Code: 2, Err: errors.New("version accepts no arguments")}
		}
		fmt.Fprintln(stdout, version)
		return nil
	case "plugin":
		return plugincommands.Run(ctx, arguments[1:], stdout, stderr)
	case "run", "daemon":
		handler := options.Run
		if handler == nil {
			handler = DaemonHandler()
		}
		return handler(ctx, arguments[1:], stdin, stdout, stderr)
	case "login":
		handler := options.Login
		if handler == nil {
			handler = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				return ErrLoginNotConfigured
			}
		}
		return handler(ctx, arguments[1:], stdin, stdout, stderr)
	case "config":
		if len(arguments) < 2 || arguments[1] != "check" {
			return &StatusError{Code: 2, Err: errors.New("config requires the check subcommand")}
		}
		handler := options.ConfigCheck
		if handler == nil {
			handler = DaemonHandler("-check-config")
		}
		return handler(ctx, arguments[2:], stdin, stdout, stderr)
	default:
		return &StatusError{Code: 2, Err: fmt.Errorf("unknown command %q", arguments[0])}
	}
}

// StatusError attaches a process status to a command error. SuppressReport is
// useful for an external process whose diagnostics were already forwarded.
type StatusError struct {
	Code           int
	Err            error
	SuppressReport bool
}

func (err *StatusError) Error() string {
	if err == nil || err.Err == nil {
		return "command failed"
	}
	return err.Err.Error()
}

func (err *StatusError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// ExitCode extracts a portable shell exit code from err.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status *StatusError
	if errors.As(err, &status) && status.Code > 0 && status.Code <= 255 {
		return status.Code
	}
	return 1
}
