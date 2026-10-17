// serenox is the canonical user-facing Serenox command.
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nihmadev/Serenox/internal/cli"
	"github.com/nihmadev/Serenox/internal/daemon"
)

const version = "0.1.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.Main(ctx, arguments, stdin, stdout, stderr, cli.Options{
		Version:     version,
		Run:         daemonCommand(false),
		Login:       loginCommand,
		ConfigCheck: daemonCommand(true),
	})
}

// daemonCommand runs the core in this process. serenoxd remains available as
// a standalone service entrypoint, but it is no longer a runtime dependency
// of the normal user-facing command.
func daemonCommand(checkConfig bool) cli.CommandHandler {
	return func(ctx context.Context, arguments []string, _ io.Reader, stdout, stderr io.Writer) error {
		runContext, stop := context.WithCancel(ctx)
		defer stop()
		if checkConfig {
			arguments = append([]string{"-check-config"}, arguments...)
		}
		code := daemon.RunContext(runContext, stop, arguments, stdout, stderr, daemon.DefaultTransportDependencies())
		if code == 0 {
			return nil
		}
		return &cli.StatusError{
			Code: code, Err: errors.New("Serenox runtime command failed"), SuppressReport: true,
		}
	}
}
