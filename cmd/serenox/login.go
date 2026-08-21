package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/nihmadev/Serenox/internal/bootstrap"
	"github.com/nihmadev/Serenox/internal/cli"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/telegram"
)

const maxInteractiveLineBytes = 4096

type authorizeFunc func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error)

type loginDependencies struct {
	authorize    authorizeFunc
	readPassword func(int) ([]byte, error)
}

func loginCommand(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	dependencies := loginDependencies{authorize: telegram.Authorize, readPassword: terminalPasswordReader(stdin)}
	return runLogin(ctx, arguments, stdin, stdout, stderr, dependencies)
}

func runLogin(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	dependencies loginDependencies,
) error {
	if dependencies.authorize == nil {
		return errors.New("Telegram authorization dependency is missing")
	}
	flags := flag.NewFlagSet("serenox login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to Serenox JSON configuration")
	phone := flags.String("phone", "", "Telegram phone in E.164 form (prefer SERENOX_TELEGRAM_PHONE)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: serenox login [--config path] [--phone +15551234567]")
		fmt.Fprintln(stderr, "Authorizes a native Telegram user session using phone, code, and optional 2FA.")
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &cli.StatusError{Code: 2, Err: err}
	}
	if flags.NArg() != 0 {
		return &cli.StatusError{Code: 2, Err: errors.New("login accepts no positional arguments")}
	}

	runtime, err := config.LoadWithOptions(config.LoadOptions{
		Path: *configPath,
		Override: func(value *config.Runtime) error {
			if *phone != "" {
				value.Telegram.Phone = *phone
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	liveConfig, err := bootstrap.TelegramLiveConfig(runtime)
	if err != nil {
		return err
	}
	prompt := &terminalPrompt{
		reader:       bufio.NewReaderSize(stdin, maxInteractiveLineBytes+2),
		output:       stderr,
		phone:        runtime.Telegram.Phone,
		readPassword: dependencies.readPassword,
	}
	identity, err := dependencies.authorize(ctx, liveConfig, prompt)
	if err != nil {
		return fmt.Errorf("Telegram authorization: %w", err)
	}
	if identity.UserID == 0 {
		return errors.New("Telegram authorization returned an empty account identity")
	}
	username := ""
	if identity.Username != "" {
		username = " username=@" + identity.Username
	}
	fmt.Fprintf(stdout, "Telegram authorization OK: user_id=%d%s session=%s\n", identity.UserID, username, runtime.TelegramSessionPath())
	if !runtime.Telegram.Enabled {
		fmt.Fprintln(stdout, "Telegram transport remains disabled; set telegram.enabled=true before serenox run.")
	}
	return nil
}

type terminalPrompt struct {
	reader       *bufio.Reader
	output       io.Writer
	phone        string
	readPassword func(int) ([]byte, error)
}

func (p *terminalPrompt) Phone(ctx context.Context) (string, error) {
	phone := p.phone
	if phone == "" {
		var err error
		phone, err = p.readLine(ctx, "Phone (E.164): ")
		if err != nil {
			return "", err
		}
	}
	if !validE164Phone(phone) {
		return "", errors.New("phone must use E.164 form: + followed by 7 to 15 digits")
	}
	return phone, nil
}

func (p *terminalPrompt) Code(ctx context.Context, metadata telegram.CodePrompt) (string, error) {
	details := metadata.Delivery
	if details == "" {
		details = "unknown"
	}
	if metadata.Length > 0 {
		details = fmt.Sprintf("%s, %d characters", details, metadata.Length)
	}
	return p.readLine(ctx, fmt.Sprintf("Telegram code (%s): ", details))
}

func (p *terminalPrompt) Password(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.readPassword == nil {
		return "", errors.New("2FA password requires an interactive terminal; run serenox login from a terminal")
	}
	if _, err := io.WriteString(p.output, "Telegram 2FA password: "); err != nil {
		return "", err
	}
	payload, err := p.readPassword(maxInteractiveLineBytes)
	defer clear(payload)
	_, _ = io.WriteString(p.output, "\n")
	if err != nil {
		return "", err
	}
	if len(payload) == 0 {
		return "", errors.New("2FA password must not be empty")
	}
	if len(payload) > maxInteractiveLineBytes {
		return "", errors.New("2FA password exceeds input limit")
	}
	return string(payload), nil
}

func validE164Phone(value string) bool {
	if len(value) < 8 || len(value) > 16 || value[0] != '+' || value[1] == '0' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (p *terminalPrompt) readLine(ctx context.Context, label string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := io.WriteString(p.output, label); err != nil {
		return "", err
	}
	payload, err := p.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errors.New("interactive input exceeds limit")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line := strings.TrimSuffix(string(payload), "\n")
	line = strings.TrimSuffix(line, "\r")
	if len(line) > maxInteractiveLineBytes {
		return "", errors.New("interactive input exceeds limit")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("interactive input must not be empty")
	}
	return line, nil
}

func terminalPasswordReader(stdin io.Reader) func(int) ([]byte, error) {
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil
	}
	return func(limit int) ([]byte, error) {
		payload, err := term.ReadPassword(int(file.Fd()))
		if len(payload) > limit {
			for i := range payload {
				payload[i] = 0
			}
			return nil, errors.New("2FA password exceeds input limit")
		}
		return payload, err
	}
}
