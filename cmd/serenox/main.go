// serenox is the canonical user-facing Serenox command.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nihmadev/Serenox/internal/cli"
)

const version = "0.1.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.Main(ctx, arguments, stdin, stdout, stderr, cli.Options{Version: version, Login: loginCommand})
}
