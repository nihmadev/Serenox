package bootstrap

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/nihmadev/Serenox/internal/config"
)

func TestTelegramLiveConfig(t *testing.T) {
	runtime := config.Defaults()
	runtime.DataDir = t.TempDir()
	runtime.Telegram.APIID = 12345
	runtime.Telegram.APIHash = "0123456789abcdef0123456789abcdef"

	live, err := TelegramLiveConfig(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if live.AppID != runtime.Telegram.APIID || live.AppHash != runtime.Telegram.APIHash || live.Session == nil {
		t.Fatalf("unexpected live config: app_id=%d hash_set=%t session_set=%t", live.AppID, live.AppHash != "", live.Session != nil)
	}
	if live.Device.DeviceModel != runtime.Telegram.DeviceModel || live.Device.LangCode != runtime.Telegram.LangCode {
		t.Fatalf("unexpected device config: %#v", live.Device)
	}
	if err := live.Session.Save([]byte("session")); err != nil {
		t.Fatal(err)
	}
	payload, err := live.Session.Load()
	if err != nil || string(payload) != "session" {
		t.Fatalf("session: payload=%q err=%v", payload, err)
	}
	if got, want := runtime.TelegramSessionPath(), filepath.Join(runtime.DataDir, runtime.Telegram.SessionFile); got != want {
		t.Fatalf("session path=%q want=%q", got, want)
	}
}

func TestTelegramLiveConfigRequiresCredentialsWithoutLeakingValues(t *testing.T) {
	runtime := config.Defaults()
	_, err := TelegramLiveConfig(runtime)
	if !errors.Is(err, ErrTelegramCredentialsRequired) {
		t.Fatalf("error=%v", err)
	}
}
