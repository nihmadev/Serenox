package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestVersionAndConfigCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-version"}, &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) != version {
		t.Fatalf("version: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-check-config"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "configuration OK") {
		t.Fatalf("check: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestHelpAndPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"unexpected"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "no positional") {
		t.Fatalf("positional: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestConfigCheckAppliesEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SERENOX_TELEGRAM_API_ID", "not-an-integer")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-check-config"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "SERENOX_TELEGRAM_API_ID") {
		t.Fatalf("config check: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type fakeTransport struct{}

func (*fakeTransport) Run(context.Context, telegram.Sink) error { return nil }
func (*fakeTransport) Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error) {
	return pluginv1.ActionResult{}, nil
}
func (*fakeTransport) Close(context.Context) error { return nil }

func TestSelectLiveTelegramUsesSavedIdentityWithoutPrompt(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = t.TempDir()
	runtime.Telegram.Enabled = true
	runtime.Telegram.APIID = 12345
	runtime.Telegram.APIHash = "0123456789abcdef0123456789abcdef"
	wantTransport := &fakeTransport{}
	authorized := false
	created := false
	dependencies := transportDependencies{
		authorize: func(_ context.Context, live telegram.LiveConfig, prompt telegram.Prompt) (telegram.Identity, error) {
			authorized = true
			if prompt != nil || live.Session == nil || live.AppID != runtime.Telegram.APIID {
				t.Fatalf("prompt=%T live=%#v", prompt, live)
			}
			return telegram.Identity{UserID: 777, Username: "owner"}, nil
		},
		newLive: func(live telegram.LiveConfig) (telegram.Transport, error) {
			created = true
			if live.Session == nil {
				t.Fatal("missing session")
			}
			return wantTransport, nil
		},
	}
	transport, selfID, err := selectTelegramTransport(context.Background(), runtime, "", 0, dependencies)
	if err != nil || transport != wantTransport || selfID != 777 || !authorized || !created {
		t.Fatalf("transport=%T self_id=%d authorized=%t created=%t err=%v", transport, selfID, authorized, created, err)
	}
}

func TestSelectLiveTelegramRequiresLoginAndRejectsIdentityMismatch(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = t.TempDir()
	runtime.Telegram.Enabled = true
	runtime.Telegram.APIID = 12345
	runtime.Telegram.APIHash = "0123456789abcdef0123456789abcdef"
	dependencies := transportDependencies{
		authorize: func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
			return telegram.Identity{}, telegram.ErrAuthPromptRequired
		},
		newLive: func(telegram.LiveConfig) (telegram.Transport, error) { return &fakeTransport{}, nil },
	}
	_, _, err := selectTelegramTransport(context.Background(), runtime, "", 0, dependencies)
	if err == nil || !strings.Contains(err.Error(), "serenox login") {
		t.Fatalf("unauthorized error=%v", err)
	}
	dependencies.authorize = func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
		return telegram.Identity{}, errors.New("decode saved session")
	}
	_, _, err = selectTelegramTransport(context.Background(), runtime, "", 0, dependencies)
	if err == nil || !strings.Contains(err.Error(), "serenox login") || !strings.Contains(err.Error(), "decode saved session") {
		t.Fatalf("corrupt session error=%v", err)
	}
	dependencies.authorize = func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error) {
		return telegram.Identity{UserID: 777}, nil
	}
	_, _, err = selectTelegramTransport(context.Background(), runtime, "", 888, dependencies)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error=%v", err)
	}
}

func TestSelectRecordedStillRequiresExplicitSelfID(t *testing.T) {
	_, _, err := selectTelegramTransport(context.Background(), config.Defaults(), "fixture.ndjson", 0, transportDependencies{})
	if err == nil || !strings.Contains(err.Error(), "self-id") {
		t.Fatalf("error=%v", err)
	}
}

func TestRecordedRun(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "updates.ndjson")
	content := `{"id":"ping","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":2,"sender_id":7,"date_unix_ms":1,"text":".ping","outgoing":true}}` + "\n"
	if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	configPath := filepath.Join(t.TempDir(), "config.json")
	configJSON := `{"schema_version":1,"data_dir":"` + dataDir + `","log_level":"error","shutdown_timeout":"1s","storage":{"path":"state.json","max_namespaces":16,"max_keys_per_namespace":64,"max_total_entries":256,"max_namespace_bytes":64,"max_key_bytes":64,"max_value_bytes":4096,"max_file_bytes":1048576},"execution":{"max_plugins":32,"queue_capacity":8,"workers_per_plugin":1,"handler_timeout":"1s"},"rate_limit":{"tokens_per_second":100,"burst":10,"max_identities":64,"idle_ttl":"1s","sweep_interval":"100ms","shards":4},"dispatch":{"command_prefix":".","max_commands":128,"max_aliases":128,"max_watchers":128}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-config", configPath, "-recorded", fixture, "-self-id", "7"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
