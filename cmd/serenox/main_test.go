package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/telegram"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Fatalf("version=%q", got)
	}
}

func TestLoginAuthorizesAndPersistsSessionWithoutPrintingSecrets(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = filepath.Join(t.TempDir(), "data")
	runtime.Telegram.Enabled = true
	runtime.Telegram.APIID = 12345
	runtime.Telegram.APIHash = "0123456789abcdef0123456789abcdef"
	configPath := writeRuntimeConfig(t, runtime)

	const (
		phone    = "+15551234567"
		code     = "24680"
		password = "do-not-print-this"
	)
	called := false
	dependencies := loginDependencies{
		authorize: func(ctx context.Context, live telegram.LiveConfig, prompt telegram.Prompt) (telegram.Identity, error) {
			called = true
			if live.AppID != runtime.Telegram.APIID || live.AppHash != runtime.Telegram.APIHash {
				t.Fatal("live credentials were not mapped")
			}
			gotPhone, err := prompt.Phone(ctx)
			if err != nil || gotPhone != phone {
				t.Fatalf("phone=%q err=%v", gotPhone, err)
			}
			gotCode, err := prompt.Code(ctx, telegram.CodePrompt{Delivery: "telegram_app", Length: 5})
			if err != nil || gotCode != code {
				t.Fatalf("code=%q err=%v", gotCode, err)
			}
			gotPassword, err := prompt.Password(ctx)
			if err != nil || gotPassword != password {
				t.Fatalf("password length=%d err=%v", len(gotPassword), err)
			}
			if err := live.Session.Save([]byte("authorized-session")); err != nil {
				t.Fatal(err)
			}
			return telegram.Identity{UserID: 777, Username: "owner"}, nil
		},
		readPassword: func(limit int) ([]byte, error) {
			if limit != maxInteractiveLineBytes {
				t.Fatalf("password limit=%d", limit)
			}
			return []byte(password), nil
		},
	}
	var stdout, stderr bytes.Buffer
	err := runLogin(context.Background(), []string{"--config", configPath, "--phone", phone}, strings.NewReader(code+"\n"), &stdout, &stderr, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(stdout.String(), "user_id=777") || !strings.Contains(stdout.String(), "username=@owner") {
		t.Fatalf("called=%t stdout=%q", called, stdout.String())
	}
	combined := stdout.String() + stderr.String()
	for _, secret := range []string{runtime.Telegram.APIHash, phone, code, password} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret appeared in output: %q", combined)
		}
	}
	payload, err := os.ReadFile(runtime.TelegramSessionPath())
	if err != nil || string(payload) != "authorized-session" {
		t.Fatalf("session payload=%q err=%v", payload, err)
	}
	if info, err := os.Stat(runtime.TelegramSessionPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode: info=%v err=%v", info, err)
	}
}

func TestLoginHelpAndCredentialError(t *testing.T) {
	called := false
	dependencies := loginDependencies{authorize: func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
		called = true
		return telegram.Identity{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := runLogin(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr, dependencies); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(stderr.String(), "Usage: serenox login") {
		t.Fatalf("called=%t stderr=%q", called, stderr.String())
	}

	stderr.Reset()
	err := runLogin(context.Background(), nil, strings.NewReader(""), &stdout, &stderr, dependencies)
	if err == nil || !strings.Contains(err.Error(), "Telegram API ID") {
		t.Fatalf("error=%v", err)
	}
}

func TestLoginPromptsForMissingAPICredentialsWithoutChangingJSON(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = filepath.Join(t.TempDir(), "data")
	configPath := writeRuntimeConfig(t, runtime)
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	const apiHash = "0123456789abcdef0123456789abcdef"
	readPasswordCalls := 0
	dependencies := loginDependencies{
		authorize: func(_ context.Context, live telegram.LiveConfig, prompt telegram.Prompt) (telegram.Identity, error) {
			if live.AppID != 12345 || live.AppHash != apiHash {
				t.Fatalf("live credentials = (%d, %q)", live.AppID, live.AppHash)
			}
			if phone, err := prompt.Phone(context.Background()); err != nil || phone != "+15551234567" {
				t.Fatalf("phone=%q err=%v", phone, err)
			}
			return telegram.Identity{UserID: 777}, nil
		},
		readPassword: func(limit int) ([]byte, error) {
			readPasswordCalls++
			if limit != maxInteractiveLineBytes {
				t.Fatalf("password limit=%d", limit)
			}
			return []byte(apiHash), nil
		},
	}
	var stdout, stderr bytes.Buffer
	err = runLogin(
		context.Background(),
		[]string{"--config", configPath, "--phone", "+15551234567"},
		strings.NewReader("12345\n"),
		&stdout,
		&stderr,
		dependencies,
	)
	if err != nil {
		t.Fatal(err)
	}
	if readPasswordCalls != 1 || !strings.Contains(stderr.String(), "Telegram API ID") || !strings.Contains(stderr.String(), "Telegram API hash") {
		t.Fatalf("prompts=%q readPasswordCalls=%d", stderr.String(), readPasswordCalls)
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("configuration was changed: err=%v before=%q after=%q", err, original, got)
	}
	if strings.Contains(stdout.String()+stderr.String(), apiHash) {
		t.Fatalf("API hash appeared in output: %q", stdout.String()+stderr.String())
	}
}

func TestLoginCollectsPhoneBeforeStartingAuthorization(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = filepath.Join(t.TempDir(), "data")
	configPath := writeRuntimeConfig(t, runtime)
	const apiHash = "0123456789abcdef0123456789abcdef"
	authorizeCalled := false
	dependencies := loginDependencies{
		authorize: func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
			authorizeCalled = true
			return telegram.Identity{UserID: 1}, nil
		},
		readPassword: func(int) ([]byte, error) { return []byte(apiHash), nil },
	}
	var stdout, stderr bytes.Buffer
	err := runLogin(
		context.Background(), []string{"--config", configPath},
		strings.NewReader("12345\n"), &stdout, &stderr, dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "Telegram phone") || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("phone was not rejected before authorization: err=%v", err)
	}
	if authorizeCalled {
		t.Fatal("authorization started before phone prompt")
	}
	if !strings.Contains(stderr.String(), "Phone (E.164): ") {
		t.Fatalf("phone prompt was not rendered: %q", stderr.String())
	}
}

func TestTerminalPromptRetriesInvalidAPICredentials(t *testing.T) {
	const validHash = "fedcba9876543210fedcba9876543210"
	hashValues := [][]byte{[]byte("wrong"), []byte(validHash)}
	prompt := &terminalPrompt{
		reader: bufioReader(strings.NewReader("not-an-id\n12345\n")),
		output: io.Discard,
		readPassword: func(int) ([]byte, error) {
			value := hashValues[0]
			hashValues = hashValues[1:]
			return value, nil
		},
	}
	apiID, err := prompt.APIID(context.Background())
	if err != nil || apiID != 12345 {
		t.Fatalf("apiID=%d err=%v", apiID, err)
	}
	apiHash, err := prompt.APIHash(context.Background())
	if err != nil || apiHash != validHash {
		t.Fatalf("apiHash=%q err=%v", apiHash, err)
	}
}

func TestTerminalPromptAcceptsBracketedPasteMarkers(t *testing.T) {
	const apiHash = "0123456789abcdef0123456789abcdef"
	prompt := &terminalPrompt{
		reader: strings.NewReader("\x1b[200~12345\x1b[201~\n"),
		output: io.Discard,
		readPassword: func(int) ([]byte, error) {
			return []byte("\x1b[200~" + apiHash + "\x1b[201~"), nil
		},
	}
	apiID, err := prompt.APIID(context.Background())
	if err != nil || apiID != 12345 {
		t.Fatalf("apiID=%d err=%v", apiID, err)
	}
	gotHash, err := prompt.APIHash(context.Background())
	if err != nil || gotHash != apiHash {
		t.Fatalf("apiHash=%q err=%v", gotHash, err)
	}
}

func TestLoginRejectsEmptyAuthorizedIdentity(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = t.TempDir()
	runtime.Telegram.APIID = 12345
	runtime.Telegram.APIHash = "0123456789abcdef0123456789abcdef"
	configPath := writeRuntimeConfig(t, runtime)
	dependencies := loginDependencies{authorize: func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
		return telegram.Identity{}, nil
	}}
	var stdout, stderr bytes.Buffer
	err := runLogin(context.Background(), []string{"--config", configPath, "--phone", "+15551234567"}, strings.NewReader(""), &stdout, &stderr, dependencies)
	if err == nil || !strings.Contains(err.Error(), "empty account identity") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestTerminalPromptBoundsInputAndRequiresTTYForPassword(t *testing.T) {
	prompt := &terminalPrompt{
		reader: bufioReader(strings.NewReader(strings.Repeat("1", maxInteractiveLineBytes+1) + "\n")),
		output: io.Discard,
	}
	if _, err := prompt.Code(context.Background(), telegram.CodePrompt{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized input error=%v", err)
	}
	if _, err := prompt.Password(context.Background()); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("password error=%v", err)
	}
}

func TestTerminalPromptRejectsInvalidInteractivePhone(t *testing.T) {
	prompt := &terminalPrompt{
		reader: bufioReader(strings.NewReader("555 secret\n")),
		output: io.Discard,
	}
	if _, err := prompt.Phone(context.Background()); err == nil || !strings.Contains(err.Error(), "E.164") {
		t.Fatalf("phone error=%v", err)
	}
}

func TestTerminalPromptNormalizesPhoneFormats(t *testing.T) {
	for input, want := range map[string]string{
		"+1 323 705 3776":    "+13237053776",
		"1-323-705-3776":     "+13237053776",
		"001 (323) 705-3776": "+13237053776",
	} {
		prompt := &terminalPrompt{
			reader: bufioReader(strings.NewReader(input + "\n")),
			output: io.Discard,
		}
		got, err := prompt.Phone(context.Background())
		if err != nil || got != want {
			t.Errorf("Phone(%q)=%q, err=%v; want %q", input, got, err, want)
		}
	}
}

func TestTerminalPromptClearsPasswordBuffer(t *testing.T) {
	payload := []byte("temporary-secret")
	prompt := &terminalPrompt{
		reader: bufioReader(strings.NewReader("")),
		output: io.Discard,
		readPassword: func(int) ([]byte, error) {
			return payload, nil
		},
	}
	password, err := prompt.Password(context.Background())
	if err != nil || password != "temporary-secret" {
		t.Fatalf("password length=%d err=%v", len(password), err)
	}
	for index, value := range payload {
		if value != 0 {
			t.Fatalf("password buffer byte %d was not cleared", index)
		}
	}
}

func writeRuntimeConfig(t *testing.T, runtime config.Runtime) string {
	t.Helper()
	payload, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bufioReader(reader *strings.Reader) *bufio.Reader {
	return bufio.NewReaderSize(reader, maxInteractiveLineBytes+2)
}
