package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	serenox "github.com/nihmadev/Serenox/sdk/go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serenox.Run(ctx, newAutopilotHandler(nil), serenox.Config{Workers: 2, QueueSize: 32})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "autopilot:", err)
		os.Exit(1)
	}
}
