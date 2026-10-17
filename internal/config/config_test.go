package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func TestDefaultsValidateAndConvert(t *testing.T) {
	t.Parallel()
	c := Defaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StorageOptions(); err != nil {
		t.Fatal(err)
	}
	if cfg, err := c.ExecutorConfig(); err != nil || cfg.Workers != c.Execution.WorkersPerPlugin {
		t.Fatalf("ExecutorConfig = %#v, %v", cfg, err)
	}
	if cfg, err := c.RateLimiterConfig(); err != nil || cfg.MaxIdentities != c.RateLimit.MaxIdentities {
		t.Fatalf("RateLimiterConfig = %#v, %v", cfg, err)
	}
}

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"data_dir":"from-file","execution":{"max_plugins":10,"queue_capacity":4,"workers_per_plugin":1,"handler_timeout":"2s"},"dispatch":{"command_prefix":"!","max_commands":100,"max_aliases":100,"max_watchers":100}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SERENOX_DATA_DIR": "from-env", "SERENOX_QUEUE_CAPACITY": "8", "SERENOX_HANDLER_TIMEOUT": "3s"}
	c, err := LoadWithOptions(LoadOptions{Path: path, LookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok }, Override: func(c *Runtime) error { c.DataDir = "from-cli"; c.Execution.QueueCapacity = 16; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "from-cli" || c.Execution.QueueCapacity != 16 || c.Execution.HandlerTimeout.Duration() != 3*time.Second || c.Dispatch.CommandPrefix != "!" {
		t.Fatalf("loaded config = %#v", c)
	}
	if c.Storage.MaxNamespaces == 0 {
		t.Fatal("partial nested object erased defaults")
	}
}

func TestLoadAllowsIncompleteTelegramCredentialsOnlyForInteractiveCommands(t *testing.T) {
	t.Parallel()
	c, err := LoadWithOptions(LoadOptions{
		LookupEnv:                          noEnv,
		AllowIncompleteTelegramCredentials: true,
		Override: func(value *Runtime) error {
			value.Telegram.Enabled = true
			value.Telegram.APIID = 123456
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.APIID != 123456 || c.Telegram.APIHash != "" {
		t.Fatalf("loaded Telegram credentials = %+v", c.Telegram)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "api_id and api_hash must be set together") {
		t.Fatalf("complete validation error = %v", err)
	}
}

func TestLoadRejectsUnknownTrailingAndOversize(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"unknown":  []byte(`{"unknown":true}`),
		"trailing": []byte(`{} {}`),
		"oversize": make([]byte, 1024*1024+1),
	} {
		path := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWithOptions(LoadOptions{Path: path, LookupEnv: noEnv}); err == nil {
			t.Errorf("%s input accepted", name)
		}
	}
}

func TestExampleConfigurationLoads(t *testing.T) {
	t.Parallel()
	c, err := LoadWithOptions(LoadOptions{Path: filepath.Join("..", "..", "config.example.json"), LookupEnv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Telegram.Enabled || c.Telegram.SessionFile != "telegram.session.json" {
		t.Fatalf("example Telegram config = %+v", c.Telegram)
	}
}

func TestValidationReportsField(t *testing.T) {
	t.Parallel()
	c := Defaults()
	c.Execution.WorkersPerPlugin = -1
	err := c.Validate()
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "execution" {
		t.Fatalf("error = %#v", err)
	}
	c = Defaults()
	c.Dispatch.CommandPrefix = "bad prefix"
	if err := c.Validate(); !errors.As(err, &fieldErr) {
		t.Fatalf("prefix error = %v", err)
	}
}

func TestEnvironmentParsingErrorIsActionable(t *testing.T) {
	t.Parallel()
	_, err := LoadWithOptions(LoadOptions{LookupEnv: func(key string) (string, bool) {
		if key == "SERENOX_MAX_PLUGINS" {
			return "many", true
		}
		return "", false
	}})
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "SERENOX_MAX_PLUGINS" {
		t.Fatalf("error = %#v", err)
	}
}

func TestDurationJSON(t *testing.T) {
	t.Parallel()
	original := Duration(1500 * time.Millisecond)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"1.5s"` {
		t.Fatalf("marshaled duration = %s", data)
	}
	var decoded Duration
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("decoded = %s", decoded)
	}
	if err := json.Unmarshal([]byte(`1500`), &decoded); err == nil {
		t.Fatal("numeric duration accepted")
	}
}

func TestStoragePath(t *testing.T) {
	t.Parallel()
	c := Defaults()
	c.DataDir = filepath.Join("var", "serenox")
	c.Storage.Path = "state.json"
	if got, want := c.StoragePath(), filepath.Join("var", "serenox", "state.json"); got != want {
		t.Fatalf("StoragePath = %q, want %q", got, want)
	}
	abs := filepath.Join(string(filepath.Separator), "tmp", "state.json")
	c.Storage.Path = abs
	if got := c.StoragePath(); got != filepath.Clean(abs) {
		t.Fatalf("absolute StoragePath = %q", got)
	}
}

func TestPluginConfigurationAndTrustPath(t *testing.T) {
	t.Parallel()
	c := Defaults()
	c.DataDir = filepath.Join("var", "serenox")
	c.Plugins.TrustFile = "trusted-keys.json"
	if got, want := c.PluginTrustPath(), filepath.Join("var", "serenox", "trusted-keys.json"); got != want {
		t.Fatalf("PluginTrustPath = %q, want %q", got, want)
	}
	c.Plugins.RetainVersions = 1
	var fieldErr *FieldError
	if err := c.Validate(); !errors.As(err, &fieldErr) || fieldErr.Field != "plugins.retain_versions" {
		t.Fatalf("plugin retention validation = %#v", err)
	}
}

func TestTelegramDefaultsAndSessionPath(t *testing.T) {
	t.Parallel()
	c := Defaults()
	if !c.Telegram.Enabled {
		t.Fatal("Telegram transport is disabled by default")
	}
	if c.Telegram.AuthMode != TelegramAuthPhone || c.Telegram.SessionFile != "telegram.session.json" {
		t.Fatalf("Telegram defaults = %+v", c.Telegram)
	}
	c.DataDir = filepath.Join("var", "serenox")
	if got, want := c.TelegramSessionPath(), filepath.Join("var", "serenox", "telegram.session.json"); got != want {
		t.Fatalf("TelegramSessionPath = %q, want %q", got, want)
	}
	abs := filepath.Join(string(filepath.Separator), "tmp", "account.session.json")
	c.Telegram.SessionFile = abs
	if got := c.TelegramSessionPath(); got != filepath.Clean(abs) {
		t.Fatalf("absolute TelegramSessionPath = %q", got)
	}
}

func TestTelegramValidation(t *testing.T) {
	t.Parallel()
	valid := func() Runtime {
		c := Defaults()
		c.Telegram.Enabled = true
		c.Telegram.APIID = 123456
		c.Telegram.APIHash = "0123456789abcdef0123456789abcdef"
		c.Telegram.Phone = "+15551234567"
		return c
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid Telegram config rejected: %v", err)
	}
	tests := []struct {
		name  string
		field string
		edit  func(*Runtime)
	}{
		{"credential pair", "telegram", func(c *Runtime) { c.Telegram.APIHash = "" }},
		{"API ID range", "telegram.api_id", func(c *Runtime) { c.Telegram.APIID = -1 }},
		{"API hash", "telegram.api_hash", func(c *Runtime) { c.Telegram.APIHash = "not-a-secret-value" }},
		{"session traversal", "telegram.session_file", func(c *Runtime) { c.Telegram.SessionFile = filepath.Join("..", "account.session.json") }},
		{"unsupported auth mode", "telegram.auth_mode", func(c *Runtime) { c.Telegram.AuthMode = "qr" }},
		{"blank device", "telegram.device_model", func(c *Runtime) { c.Telegram.DeviceModel = " " }},
		{"language", "telegram.lang_code", func(c *Runtime) { c.Telegram.LangCode = "EN" }},
		{"locale", "telegram.system_lang_code", func(c *Runtime) { c.Telegram.SystemLangCode = "en--US" }},
		{"phone", "SERENOX_TELEGRAM_PHONE", func(c *Runtime) { c.Telegram.Phone = "555 secret" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := valid()
			test.edit(&c)
			var fieldErr *FieldError
			if err := c.Validate(); !errors.As(err, &fieldErr) || fieldErr.Field != test.field {
				t.Fatalf("error = %#v, want field %q", err, test.field)
			}
		})
	}
}

func TestNormalizeTelegramPhone(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"+1 323 705 3776":    "+13237053776",
		"1-323-705-3776":     "+13237053776",
		"001 (323) 705-3776": "+13237053776",
	} {
		got, err := NormalizeTelegramPhone(input)
		if err != nil || got != want {
			t.Errorf("NormalizeTelegramPhone(%q)=%q, err=%v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"+1 323 secret", "+0001234567", "++++", ""} {
		if got, err := NormalizeTelegramPhone(input); err == nil {
			t.Errorf("NormalizeTelegramPhone(%q)=%q accepted", input, got)
		}
	}
}

func TestTelegramEnvironmentOverridesAndRedaction(t *testing.T) {
	t.Parallel()
	secretHash := "fedcba9876543210fedcba9876543210"
	secretPhone := "+15559876543"
	env := map[string]string{
		"SERENOX_TELEGRAM_ENABLED":          "true",
		"SERENOX_TELEGRAM_API_ID":           "654321",
		"SERENOX_TELEGRAM_API_HASH":         secretHash,
		"SERENOX_TELEGRAM_SESSION_FILE":     "sessions/owner.json",
		"SERENOX_TELEGRAM_DEVICE_MODEL":     "Serenox Desktop",
		"SERENOX_TELEGRAM_APP_VERSION":      "1.2.3",
		"SERENOX_TELEGRAM_SYSTEM_VERSION":   "Linux",
		"SERENOX_TELEGRAM_LANG_CODE":        "ru",
		"SERENOX_TELEGRAM_SYSTEM_LANG_CODE": "ru-RU",
		"SERENOX_TELEGRAM_PHONE":            secretPhone,
	}
	c, err := LoadWithOptions(LoadOptions{LookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok }})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Telegram.Enabled || c.Telegram.APIID != 654321 || c.Telegram.APIHash != secretHash || c.Telegram.Phone != secretPhone {
		t.Fatalf("Telegram environment overrides were not applied: %+v", c.Telegram)
	}
	for _, rendered := range []string{fmt.Sprintf("%+v", c.Telegram), fmt.Sprintf("%#v", c.Telegram), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(rendered, secretHash) || strings.Contains(rendered, secretPhone) {
			t.Fatalf("formatted configuration leaked a secret: %s", rendered)
		}
	}
}

func TestTelegramPhoneIsNotAcceptedFromJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"telegram":{"phone":"+15551234567"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithOptions(LoadOptions{Path: path, LookupEnv: noEnv}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("phone JSON field error = %v", err)
	}
}

func TestTelegramEnvironmentErrorsAreActionableAndRedacted(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"SERENOX_TELEGRAM_ENABLED": "sometimes",
		"SERENOX_TELEGRAM_API_ID":  "account-id",
	} {
		name, value := name, value
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithOptions(LoadOptions{LookupEnv: func(key string) (string, bool) {
				if key == name {
					return value, true
				}
				return "", false
			}})
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != name || strings.Contains(err.Error(), value) {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestOverrideErrorIsWrapped(t *testing.T) {
	t.Parallel()
	_, err := LoadWithOptions(LoadOptions{LookupEnv: noEnv, Override: func(*Runtime) error { return errors.New("bad flag") }})
	if err == nil || !strings.Contains(err.Error(), "override") {
		t.Fatalf("error = %v", err)
	}
}
