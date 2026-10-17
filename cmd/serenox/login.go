package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
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
	terminal     *os.File
}

func loginCommand(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var terminal *os.File
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		terminal = file
	}
	dependencies := loginDependencies{authorize: telegram.Authorize, readPassword: terminalPasswordReader(stdin, stderr), terminal: terminal}
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
	apiID := flags.String("api-id", "", "Telegram API ID (prefer the interactive prompt or SERENOX_TELEGRAM_API_ID)")
	phone := flags.String("phone", "", "Telegram phone in E.164 form (prefer SERENOX_TELEGRAM_PHONE)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: serenox login [--config path] [--api-id 123456] [--phone +15551234567]")
		fmt.Fprintln(stderr, "Authorizes a native Telegram user session using API credentials, phone, code, and optional 2FA.")
		fmt.Fprintln(stderr, "Missing API credentials are requested interactively; the API hash is not echoed and is never written to JSON.")
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
		Path: *configPath, AllowIncompleteTelegramCredentials: true, LoadLocalTelegramCredentials: true,
		Override: func(value *config.Runtime) error {
			if *apiID != "" {
				parsed, parseErr := parseTelegramAPIID(*apiID)
				if parseErr != nil {
					return parseErr
				}
				value.Telegram.APIID = parsed
			}
			if *phone != "" {
				value.Telegram.Phone = *phone
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	prompt := &terminalPrompt{
		reader:       stdin,
		output:       stderr,
		phone:        runtime.Telegram.Phone,
		readPassword: dependencies.readPassword,
		terminal:     dependencies.terminal,
	}
	if runtime.Telegram.APIID == 0 {
		runtime.Telegram.APIID, err = prompt.APIID(ctx)
		if err != nil {
			return fmt.Errorf("Telegram API ID: %w", err)
		}
	}
	if runtime.Telegram.APIHash == "" {
		runtime.Telegram.APIHash, err = prompt.APIHash(ctx)
		if err != nil {
			return fmt.Errorf("Telegram API hash: %w", err)
		}
	}
	if err := runtime.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	// On a first login, collect and validate the phone before starting the
	// Telegram client. The gotd auth callback may run only after a network/
	// status check, which can delay (or obscure) the phone prompt and turn an
	// empty input into a misleading callback error. A saved session does not
	// need a phone at all unless Telegram later asks for one.
	if _, statErr := os.Stat(runtime.TelegramSessionPath()); errors.Is(statErr, os.ErrNotExist) {
		runtime.Telegram.Phone, err = prompt.Phone(ctx)
		if err != nil {
			return fmt.Errorf("Telegram phone: %w", err)
		}
	}
	liveConfig, err := bootstrap.TelegramLiveConfig(runtime)
	if err != nil {
		return err
	}
	identity, err := dependencies.authorize(ctx, liveConfig, prompt)
	if err != nil {
		return fmt.Errorf("Telegram authorization: %w", err)
	}
	if identity.UserID == 0 {
		return errors.New("Telegram authorization returned an empty account identity")
	}
	if err := config.SaveTelegramCredentials(runtime.DataDir, config.TelegramCredentials{
		APIID: runtime.Telegram.APIID, APIHash: runtime.Telegram.APIHash,
	}); err != nil {
		return fmt.Errorf("save Telegram credentials: %w", err)
	}
	username := ""
	if identity.Username != "" {
		username = " username=@" + identity.Username
	}
	fmt.Fprintf(stdout, "Telegram authorization OK: user_id=%d%s session=%s\n", identity.UserID, username, runtime.TelegramSessionPath())
	fmt.Fprintln(stdout, "Run `serenox run` to start the userbot with this session.")
	return nil
}

type terminalPrompt struct {
	reader       io.Reader
	output       io.Writer
	phone        string
	readPassword func(int) ([]byte, error)
	terminal     *os.File
}

func (p *terminalPrompt) APIID(ctx context.Context) (int, error) {
	for {
		value, err := p.readLine(ctx, "Telegram API ID: ")
		if err != nil {
			return 0, err
		}
		parsed, err := parseTelegramAPIID(value)
		if err == nil {
			return parsed, nil
		}
		if _, writeErr := fmt.Fprintf(p.output, "Invalid Telegram API ID: %v\n", err); writeErr != nil {
			return 0, writeErr
		}
	}
}

func (p *terminalPrompt) APIHash(ctx context.Context) (string, error) {
	for {
		payload, err := p.readSecret(ctx, "Telegram API hash: ")
		if p.terminal != nil {
			_, _ = io.WriteString(p.output, "\n")
		}
		if err != nil {
			clear(payload)
			return "", err
		}
		value := strings.TrimSpace(stripBracketedPasteMarkers(string(payload)))
		clear(payload)
		if err := validateTelegramAPIHash(value); err == nil {
			return value, nil
		}
		if _, writeErr := io.WriteString(p.output, "Invalid Telegram API hash: must be exactly 32 hexadecimal characters\n"); writeErr != nil {
			return "", writeErr
		}
	}
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
	normalized, err := config.NormalizeTelegramPhone(phone)
	if err != nil {
		return "", errors.New("phone must use E.164 form: + followed by 7 to 15 digits")
	}
	return normalized, nil
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

func (p *terminalPrompt) readSecret(ctx context.Context, label string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.readPassword == nil {
		value, err := p.readLine(ctx, label)
		if err != nil {
			return nil, err
		}
		return []byte(value), nil
	}
	if _, err := io.WriteString(p.output, label); err != nil {
		return nil, err
	}
	payload, err := p.readPassword(maxInteractiveLineBytes)
	if len(payload) > maxInteractiveLineBytes {
		clear(payload)
		return nil, errors.New("interactive input exceeds limit")
	}
	return payload, err
}

func parseTelegramAPIID(value string) (int, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		value = strings.TrimPrefix(value, "+")
	}
	value = strings.ReplaceAll(value, " ", "")
	if value == "" {
		return 0, errors.New("must be a positive integer")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("must contain digits only")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil || parsed == 0 {
		return 0, errors.New("must be between 1 and 2147483647")
	}
	return int(parsed), nil
}

func validateTelegramAPIHash(value string) error {
	if len(value) != 32 {
		return errors.New("must be exactly 32 hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return errors.New("must be exactly 32 hexadecimal characters")
	}
	return nil
}

func (p *terminalPrompt) readLine(ctx context.Context, label string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := io.WriteString(p.output, label); err != nil {
		return "", err
	}
	if p.terminal != nil {
		payload, err := readTerminalInput(p.terminal, p.output, true, maxInteractiveLineBytes)
		if err != nil {
			_, _ = io.WriteString(p.output, "\n")
			return "", err
		}
		line := stripBracketedPasteMarkers(string(payload))
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			return "", errors.New("interactive input must not be empty")
		}
		return line, nil
	}
	var payload []byte
	var character [1]byte
	for {
		read, err := p.reader.Read(character[:])
		if read > 0 {
			if character[0] == '\n' {
				break
			}
			payload = append(payload, character[0])
			if len(payload) > maxInteractiveLineBytes {
				return "", errors.New("interactive input exceeds limit")
			}
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return "", err
			}
			break
		}
	}
	line := stripBracketedPasteMarkers(string(payload))
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

// readTerminalInput reads one interactive value from a terminal. Raw mode is
// used so a bracketed paste can finish on Konsole's paste-end marker without
// requiring a separate Enter key. The terminal state is restored on return.
func readTerminalInput(file *os.File, output io.Writer, echo bool, limit int) ([]byte, error) {
	if _, err := io.WriteString(output, "\x1b[?2004h"); err != nil {
		return nil, err
	}
	oldState, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		_, _ = io.WriteString(output, "\x1b[?2004l")
		return nil, err
	}
	defer func() {
		_ = term.Restore(int(file.Fd()), oldState)
		_, _ = io.WriteString(output, "\x1b[?2004l")
	}()

	const (
		pasteStart = "\x1b[200~"
		pasteEnd   = "\x1b[201~"
	)
	var value []byte
	var pending string
	inPaste := false
	var character [1]byte

	appendCharacter := func(character byte) error {
		if len(value) >= limit {
			return errors.New("interactive input exceeds limit")
		}
		value = append(value, character)
		if echo {
			if character == '\n' || character == '\r' {
				_, err := io.WriteString(output, "\r\n")
				return err
			}
			_, err := output.Write([]byte{character})
			return err
		}
		return nil
	}
	removeCharacter := func() {
		if len(value) == 0 {
			return
		}
		value = value[:len(value)-1]
		if echo {
			_, _ = io.WriteString(output, "\b \b")
		}
	}
	processCharacter := func(character byte) (bool, error) {
		if character == 3 {
			return false, errors.New("interactive input interrupted")
		}
		if character == 4 {
			if len(value) == 0 {
				return false, io.EOF
			}
			return true, nil
		}
		if !inPaste && (character == '\r' || character == '\n') {
			return true, nil
		}
		if !inPaste && (character == '\b' || character == 0x7f) {
			removeCharacter()
			return false, nil
		}
		if err := appendCharacter(character); err != nil {
			return false, err
		}
		return false, nil
	}

	finish := func() ([]byte, error) {
		if echo {
			if _, err := io.WriteString(output, "\r\n"); err != nil {
				return nil, err
			}
		}
		return value, nil
	}
	for {
		read, readErr := file.Read(character[:])
		if read == 0 {
			if readErr != nil {
				return value, readErr
			}
			continue
		}

		pending += string(character[:1])
		if pending == "\x1b" || strings.HasPrefix(pasteStart, pending) || strings.HasPrefix(pasteEnd, pending) {
			if pending == pasteStart {
				inPaste = true
				pending = ""
			}
			if pending == pasteEnd {
				if inPaste {
					inPaste = false
					pending = ""
					return finish()
				}
				pending = ""
			}
			continue
		}

		for _, pendingCharacter := range []byte(pending) {
			done, processErr := processCharacter(pendingCharacter)
			if processErr != nil {
				return value, processErr
			}
			if done {
				return finish()
			}
		}
		pending = ""
	}
}

// Konsole can wrap pasted text in bracketed-paste escape sequences. They are
// terminal control markers, not part of an API ID, hash, phone, or code.
func stripBracketedPasteMarkers(value string) string {
	value = strings.ReplaceAll(value, "\x1b[200~", "")
	return strings.ReplaceAll(value, "\x1b[201~", "")
}

func terminalPasswordReader(stdin io.Reader, output io.Writer) func(int) ([]byte, error) {
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil
	}
	return func(limit int) ([]byte, error) {
		return readTerminalInput(file, output, false, limit)
	}
}
