package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelegramCredentialsRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	want := TelegramCredentials{APIID: 30934954, APIHash: "0123456789abcdef0123456789abcdef"}
	if err := SaveTelegramCredentials(dataDir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTelegramCredentials(dataDir)
	if err != nil || got != want {
		t.Fatalf("credentials = %#v, err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(dataDir, telegramCredentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadTelegramCredentialsMissingIsEmpty(t *testing.T) {
	got, err := LoadTelegramCredentials(t.TempDir())
	if err != nil || got != (TelegramCredentials{}) {
		t.Fatalf("credentials = %#v, err=%v", got, err)
	}
}
