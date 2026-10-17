package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/daemon"
)

const eventsUsage = `Usage: serenox events <command> [options]

Commands:
  status
  list --state pending|retry_wait|dead_lettered
  inspect EVENT_ID
  retry EVENT_ID --subscription PLUGIN/HANDLER
  discard EVENT_ID --yes

Common options: --config PATH --data-dir PATH
`

func EventsHandler() CommandHandler {
	return func(ctx context.Context, arguments []string, _ io.Reader, stdout, stderr io.Writer) error {
		if len(arguments) == 0 {
			_, _ = io.WriteString(stderr, eventsUsage)
			return &StatusError{Code: 2, Err: errors.New("events command is required")}
		}
		command := arguments[0]
		positional := ""
		tail := arguments[1:]
		if command == "inspect" || command == "retry" || command == "discard" {
			if len(tail) == 0 {
				return &StatusError{Code: 2, Err: errors.New("event ID is required")}
			}
			positional, tail = tail[0], tail[1:]
		}
		flags := flag.NewFlagSet("serenox events "+command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("config", "", "Serenox configuration path")
		dataDir := flags.String("data-dir", "", "account data directory")
		state := flags.String("state", "", "delivery state")
		limit := flags.Int("limit", 100, "maximum records")
		subscription := flags.String("subscription", "", "plugin/handler delivery ID")
		yes := flags.Bool("yes", false, "confirm destructive discard")
		if err := flags.Parse(tail); err != nil {
			return &StatusError{Code: 2, Err: err, SuppressReport: true}
		}
		if flags.NArg() != 0 {
			return &StatusError{Code: 2, Err: errors.New("unexpected event command arguments")}
		}
		runtime, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		root := runtime.DataDir
		if *dataDir != "" {
			root = *dataDir
		}
		switch command {
		case "status":
			status, err := daemon.EventRuntimeStatus(ctx, root)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "retained_events=%d retained_bytes=%d pending=%d running=%d acknowledged=%d retry_wait=%d dead_lettered=%d\n", status.RetainedEvents, status.RetainedBytes, status.Pending, status.Running, status.Acknowledged, status.RetryWait, status.DeadLettered)
			return nil
		case "list":
			records, err := daemon.EventRuntimeList(ctx, root, *state, *limit)
			if err != nil {
				return err
			}
			return writeEventJSON(stdout, records)
		case "inspect":
			records, err := daemon.EventRuntimeInspect(ctx, root, positional)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				return errors.New("event was not found")
			}
			return writeEventJSON(stdout, records[0])
		case "retry":
			if *subscription == "" {
				return &StatusError{Code: 2, Err: errors.New("--subscription is required")}
			}
			return daemon.EventRuntimeRetry(ctx, root, positional, *subscription)
		case "discard":
			if !*yes {
				return &StatusError{Code: 2, Err: errors.New("--yes is required for destructive discard")}
			}
			return daemon.EventRuntimeDiscard(ctx, root, positional, true)
		default:
			return &StatusError{Code: 2, Err: fmt.Errorf("unknown events command %q", command)}
		}
	}
}

func writeEventJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
