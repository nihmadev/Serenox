// serenoxd is the standalone service entrypoint. The runtime itself lives in
// internal/daemon and is also embedded in the regular serenox CLI, so users do
// not need to install or locate a second executable.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/daemon"
	"github.com/nihmadev/Serenox/internal/telegram"
)

const version = daemon.Version

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContext(ctx, stop, arguments, stdout, stderr, defaultTransportDependencies())
}

// Keep these small adapters for the standalone daemon's package-local tests
// and for compatibility with code that used the old test seams.
type transportDependencies struct {
	loadRecorded func(string, telegram.RecordedOptions) (*telegram.RecordedTransport, error)
	authorize    func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error)
	newLive      func(telegram.LiveConfig) (telegram.Transport, error)
}

func defaultTransportDependencies() transportDependencies {
	return transportDependencies{
		loadRecorded: telegram.LoadRecorded,
		authorize:    telegram.Authorize,
		newLive: func(config telegram.LiveConfig) (telegram.Transport, error) {
			return telegram.NewLive(config)
		},
	}
}

func runContext(ctx context.Context, stop context.CancelFunc, arguments []string, stdout, stderr io.Writer, dependencies transportDependencies) int {
	return daemon.RunContext(ctx, stop, arguments, stdout, stderr, daemon.TransportDependencies{
		LoadRecorded: dependencies.loadRecorded,
		Authorize:    dependencies.authorize,
		NewLive:      dependencies.newLive,
	})
}

func selectTelegramTransport(
	ctx context.Context,
	runtimeConfig config.Runtime,
	recordedPath string,
	requestedSelfID int64,
	dependencies transportDependencies,
) (telegram.Transport, int64, error) {
	return daemon.SelectTelegramTransport(ctx, runtimeConfig, recordedPath, requestedSelfID, daemon.TransportDependencies{
		LoadRecorded: dependencies.loadRecorded,
		Authorize:    dependencies.authorize,
		NewLive:      dependencies.newLive,
	})
}
