package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/controlbot"
	"github.com/nihmadev/Serenox/internal/daemon"
)

const botUsage = `Usage: serenox bot <command> [options]

[1;93m⚠ IMPORTANT: enable Inline Mode in BotFather before using @bot queries: /setinline → select this bot.[0m

Commands:
  attach   verify a BotFather token with getMe, then store it privately
  verify   verify the currently attached token without printing it
  status   show non-secret control-bot state
  detach   stop local use and remove the stored token and polling offset

attach options:
  (no token option)   interactive setup: prompt for token and @username
  --token-stdin       read the token from stdin (for scripts)
  --token-file PATH   read the token from a mode-restricted local file
  --token TOKEN       explicit one-shot token input; never echoed
  --username NAME     require getMe to return this bot username
  --config PATH       Serenox configuration path
  --data-dir PATH     account data directory override
`

// ControlBotHandler implements offline lifecycle operations. Starting and
// stopping polling is deliberately daemon-owned; an attach performed here is
// picked up by the next daemon start and never creates a background process.
func ControlBotHandler() CommandHandler {
	return func(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if len(arguments) == 0 {
			_, _ = io.WriteString(stderr, botUsage)
			return &StatusError{Code: 2, Err: errors.New("bot command is required")}
		}
		flags := flag.NewFlagSet("serenox bot "+arguments[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("config", "", "Serenox configuration path")
		dataDir := flags.String("data-dir", "", "account data directory")
		token := flags.String("token", "", "one-shot BotFather token")
		tokenFile := flags.String("token-file", "", "file containing a BotFather token")
		tokenStdin := flags.Bool("token-stdin", false, "read BotFather token from stdin")
		username := flags.String("username", "", "expected bot username")
		if err := flags.Parse(arguments[1:]); err != nil {
			return &StatusError{Code: 2, Err: err, SuppressReport: true}
		}
		if flags.NArg() != 0 {
			return &StatusError{Code: 2, Err: errors.New("bot command accepts no positional arguments")}
		}
		runtime, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		root := runtime.DataDir
		if *dataDir != "" {
			root = *dataDir
		}
		store, err := controlbot.NewSecretStore(root)
		if err != nil {
			return err
		}
		manager, err := controlbot.NewManager(store, controlbot.ClientOptions{HTTPClient: httpClient})
		if err != nil {
			return err
		}
		switch arguments[0] {
		case "attach", "rotate":
			input := stdin
			if *token == "" && *tokenFile == "" && !*tokenStdin {
				if file, ok := stdin.(*os.File); !ok || !term.IsTerminal(int(file.Fd())) {
					input = bufio.NewReader(stdin)
				}
			}
			value, interactive, err := readBotToken(*token, *tokenFile, *tokenStdin, input, stderr)
			if err != nil {
				return err
			}
			if interactive && *username == "" {
				*username, err = promptLine(input, stderr, "Expected bot username (for example @my_serenox_bot; Enter to infer): ")
				if err != nil {
					return err
				}
			}
			verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			candidate, err := controlbot.NewClient(value, controlbot.ClientOptions{HTTPClient: httpClient})
			if err != nil {
				return err
			}
			identity, err := candidate.GetMe(verifyCtx)
			if err != nil {
				return fmt.Errorf("verify control bot token: %w", err)
			}
			if *username != "" && !strings.EqualFold(strings.TrimPrefix(*username, "@"), identity.Username) {
				return errors.New("verified bot username does not match --username; token was not stored")
			}
			if interactive {
				confirmed, confirmErr := confirmAttach(input, stderr, identity.Username)
				if confirmErr != nil {
					return confirmErr
				}
				if !confirmed {
					return errors.New("control bot attachment cancelled")
				}
			}
			identity, err = manager.Attach(verifyCtx, value)
			if err != nil {
				return fmt.Errorf("attach control bot: %w", err)
			}
			fmt.Fprintf(stdout, "control bot attached: id=%d username=@%s\n", identity.ID, identity.Username)
			// A running daemon reloads its own secret through the authenticated
			// local socket. The CLI never forwards the BotFather token to it.
			reloadCtx, reloadCancel := context.WithTimeout(ctx, 10*time.Second)
			reloadErr := daemon.ReloadControlBot(reloadCtx, root)
			reloadCancel()
			if reloadErr == nil {
				fmt.Fprintln(stdout, "control bot polling activated in the running daemon")
			} else if errors.Is(reloadErr, daemon.ErrControlUnavailable) {
				fmt.Fprintln(stdout, "control bot will start when Serenox daemon starts")
			} else {
				fmt.Fprintln(stdout, "control bot is attached; daemon activation failed, restart the daemon")
			}
			return nil
		case "verify":
			stored, err := store.Load()
			if err != nil {
				return err
			}
			client, err := controlbot.NewClient(stored, controlbot.ClientOptions{HTTPClient: httpClient})
			if err != nil {
				return err
			}
			verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			identity, err := client.GetMe(verifyCtx)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "control bot verified: id=%d username=@%s\n", identity.ID, identity.Username)
			return nil
		case "status":
			if _, err := store.Load(); err != nil {
				if errors.Is(err, controlbot.ErrNotAttached) {
					fmt.Fprintln(stdout, "control bot: detached")
					return nil
				}
				return err
			}
			offset, err := store.LoadOffset()
			if err != nil {
				return err
			}
			statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			runtimeStatus, runtimeErr := daemon.ControlBotRuntimeStatus(statusCtx, root)
			cancel()
			if runtimeErr == nil {
				state := "stopped"
				if runtimeStatus.Running {
					state = "running"
				}
				fmt.Fprintf(stdout, "control bot: attached (%s, saved_offset=%d", state, runtimeStatus.SavedOffset)
				if runtimeStatus.LastError != "" {
					fmt.Fprintf(stdout, ", last_error=%s", runtimeStatus.LastError)
				}
				fmt.Fprintln(stdout, ")")
				return nil
			}
			if !errors.Is(runtimeErr, daemon.ErrControlUnavailable) {
				return fmt.Errorf("read control-bot runtime status: %w", runtimeErr)
			}
			fmt.Fprintf(stdout, "control bot: attached (daemon not running, saved_offset=%d)\n", offset)
			return nil
		case "detach":
			if err := manager.Detach(ctx); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "control bot detached")
			return nil
		case "help", "--help", "-h":
			_, _ = io.WriteString(stdout, botUsage)
			return nil
		default:
			return &StatusError{Code: 2, Err: fmt.Errorf("unknown bot command %q", arguments[0])}
		}
	}
}

var httpClient = newHTTPClient()

func newHTTPClient() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

func readBotToken(flagValue, file string, fromStdin bool, stdin io.Reader, prompt io.Writer) (string, bool, error) {
	inputs := 0
	if flagValue != "" {
		inputs++
	}
	if file != "" {
		inputs++
	}
	if fromStdin {
		inputs++
	}
	if inputs > 1 {
		return "", false, errors.New("attach accepts only one token source")
	}
	if flagValue != "" {
		return strings.TrimSpace(flagValue), false, nil
	}
	if inputs == 0 {
		value, err := promptSecret(stdin, prompt, "BotFather token: ")
		return value, true, err
	}
	var raw []byte
	var err error
	if file != "" {
		raw, err = os.ReadFile(file)
	} else {
		raw, err = io.ReadAll(io.LimitReader(stdin, 1024))
	}
	if err != nil {
		return "", false, err
	}
	if len(raw) > 512 {
		return "", false, errors.New("control bot token input is too large")
	}
	return strings.TrimSpace(string(raw)), false, nil
}

func promptSecret(stdin io.Reader, prompt io.Writer, label string) (string, error) {
	_, _ = io.WriteString(prompt, label)
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		_, _ = io.WriteString(prompt, "\n")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	// Tests, redirected input, and non-terminal hosts still work. Operators
	// should use --token-stdin or --token-file there because echo cannot be
	// disabled for a generic Reader.
	value, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func promptLine(stdin io.Reader, prompt io.Writer, label string) (string, error) {
	_, _ = io.WriteString(prompt, label)
	value, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func confirmAttach(stdin io.Reader, prompt io.Writer, username string) (bool, error) {
	answer, err := promptLine(stdin, prompt, "Attach verified @"+username+" to this Serenox account? [y/N]: ")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
