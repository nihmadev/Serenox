package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestVersionAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr, Options{Version: "9.8.7"}); code != 0 {
		t.Fatalf("version exit = %d, stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != "9.8.7\n" {
		t.Fatalf("version output = %q", got)
	}
	stdout.Reset()
	if code := Main(context.Background(), []string{"help"}, strings.NewReader(""), &stdout, &stderr, Options{}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "serenox plugin") || !strings.Contains(stdout.String(), "config check") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestLoginHookIsInjectedWithoutSubprocess(t *testing.T) {
	input := strings.NewReader("12345\n")
	var stdout, stderr bytes.Buffer
	called := false
	login := func(ctx context.Context, arguments []string, stdin io.Reader, gotStdout, gotStderr io.Writer) error {
		called = true
		if ctx == nil || !reflect.DeepEqual(arguments, []string{"--session", "work"}) {
			t.Fatalf("login arguments = %#v", arguments)
		}
		if stdin != input || gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("login streams were not forwarded")
		}
		_, _ = io.WriteString(gotStdout, "authorized\n")
		return nil
	}
	code := Main(context.Background(), []string{"login", "--session", "work"}, input, &stdout, &stderr, Options{Login: login})
	if code != 0 || !called || stdout.String() != "authorized\n" {
		t.Fatalf("login: code=%d called=%t stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestDefaultLoginIsActionable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"login"}, strings.NewReader(""), &stdout, &stderr, Options{})
	if code != 1 || !strings.Contains(stderr.String(), "native authorization handler") {
		t.Fatalf("login: code=%d stderr=%q", code, stderr.String())
	}
	if !errors.Is(Execute(context.Background(), []string{"login"}, strings.NewReader(""), &stdout, &stderr, Options{}), ErrLoginNotConfigured) {
		t.Fatal("default login does not preserve ErrLoginNotConfigured")
	}
}

func TestRunDaemonAndConfigHandlers(t *testing.T) {
	var calls [][]string
	handler := func(_ context.Context, arguments []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string(nil), arguments...))
		return nil
	}
	options := Options{Run: handler, ConfigCheck: handler}
	for _, arguments := range [][]string{
		{"run", "--config", "one.json"},
		{"daemon", "--recorded", "updates.ndjson"},
		{"config", "check", "--config", "two.json"},
	} {
		if err := Execute(context.Background(), arguments, strings.NewReader(""), io.Discard, io.Discard, options); err != nil {
			t.Fatalf("Execute(%v): %v", arguments, err)
		}
	}
	want := [][]string{{"--config", "one.json"}, {"--recorded", "updates.ndjson"}, {"--config", "two.json"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("handler calls = %#v, want %#v", calls, want)
	}
}

func TestEventsHandlerIsInjected(t *testing.T) {
	called := false
	handler := func(_ context.Context, arguments []string, _ io.Reader, _, _ io.Writer) error {
		called = reflect.DeepEqual(arguments, []string{"status"})
		return nil
	}
	if err := Execute(context.Background(), []string{"events", "status"}, strings.NewReader(""), io.Discard, io.Discard, Options{Events: handler}); err != nil || !called {
		t.Fatalf("events dispatch: called=%t err=%v", called, err)
	}
}

func TestUsageErrorsUseExitTwo(t *testing.T) {
	for _, arguments := range [][]string{nil, {"missing"}, {"config"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := Main(context.Background(), arguments, strings.NewReader(""), &stdout, &stderr, Options{}); code != 2 {
			t.Fatalf("Main(%v) exit = %d, stderr=%q", arguments, code, stderr.String())
		}
	}
}

func TestStatusErrorCanSuppressDuplicateExternalError(t *testing.T) {
	err := &StatusError{Code: 75, Err: errors.New("already reported"), SuppressReport: true}
	var stderr bytes.Buffer
	options := Options{Run: func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return err }}
	if code := Main(context.Background(), []string{"run"}, strings.NewReader(""), io.Discard, &stderr, options); code != 75 {
		t.Fatalf("exit = %d", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("duplicate stderr = %q", stderr.String())
	}
}

func TestInteractiveControlBotPromptsDoNotPrintToken(t *testing.T) {
	const token = "123456789:abcdefghijklmnopqrstuvwxyzABCDE"
	input := bufio.NewReader(strings.NewReader(token + "\n@expected_bot\ny\n"))
	var prompts bytes.Buffer
	value, interactive, err := readBotToken("", "", false, input, &prompts)
	if err != nil || !interactive || value != token {
		t.Fatalf("token read = %q, interactive=%t, err=%v", value, interactive, err)
	}
	username, err := promptLine(input, &prompts, "Expected bot username: ")
	if err != nil || username != "@expected_bot" {
		t.Fatalf("username = %q, err=%v", username, err)
	}
	confirmed, err := confirmAttach(input, &prompts, "expected_bot")
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%t err=%v", confirmed, err)
	}
	if strings.Contains(prompts.String(), token) {
		t.Fatalf("token leaked in prompts: %q", prompts.String())
	}
}
