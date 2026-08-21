// Package config loads and validates the native core's bounded runtime settings.
package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/ratelimit"
	"github.com/nihmadev/Serenox/internal/storage"
)

const (
	CurrentSchemaVersion = 1
	TelegramAuthPhone    = "phone"
)

// Duration uses readable duration strings in JSON and environment values.
type Duration time.Duration

func (d Duration) Duration() time.Duration      { return time.Duration(d) }
func (d Duration) String() string               { return time.Duration(d).String() }
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

type Storage struct {
	Path                string `json:"path"`
	MaxNamespaces       int    `json:"max_namespaces"`
	MaxKeysPerNamespace int    `json:"max_keys_per_namespace"`
	MaxTotalEntries     int    `json:"max_total_entries"`
	MaxNamespaceBytes   int    `json:"max_namespace_bytes"`
	MaxKeyBytes         int    `json:"max_key_bytes"`
	MaxValueBytes       int    `json:"max_value_bytes"`
	MaxFileBytes        int64  `json:"max_file_bytes"`
}

type Execution struct {
	MaxPlugins       int      `json:"max_plugins"`
	QueueCapacity    int      `json:"queue_capacity"`
	WorkersPerPlugin int      `json:"workers_per_plugin"`
	HandlerTimeout   Duration `json:"handler_timeout"`
}

type RateLimit struct {
	TokensPerSecond float64  `json:"tokens_per_second"`
	Burst           float64  `json:"burst"`
	MaxIdentities   int      `json:"max_identities"`
	IdleTTL         Duration `json:"idle_ttl"`
	SweepInterval   Duration `json:"sweep_interval"`
	Shards          int      `json:"shards"`
}

type Dispatch struct {
	CommandPrefix string `json:"command_prefix"`
	MaxCommands   int    `json:"max_commands"`
	MaxAliases    int    `json:"max_aliases"`
	MaxWatchers   int    `json:"max_watchers"`
}

// Telegram configures the native user-session transport. Phone is deliberately
// excluded from JSON and can only be supplied by a short-lived environment or
// command-line override. APIHash and Phone must never be written to logs.
type Telegram struct {
	Enabled        bool   `json:"enabled"`
	APIID          int    `json:"api_id"`
	APIHash        string `json:"api_hash"`
	SessionFile    string `json:"session_file"`
	AuthMode       string `json:"auth_mode"`
	DeviceModel    string `json:"device_model"`
	AppVersion     string `json:"app_version"`
	SystemVersion  string `json:"system_version"`
	LangCode       string `json:"lang_code"`
	SystemLangCode string `json:"system_lang_code"`
	Phone          string `json:"-"`
}

// String and GoString intentionally expose only non-sensitive readiness state.
func (c Telegram) String() string {
	return fmt.Sprintf("Telegram{Enabled:%t APIIDSet:%t APIHash:<redacted> SessionFile:%q AuthMode:%q DeviceModel:%q AppVersion:%q SystemVersion:%q LangCode:%q SystemLangCode:%q Phone:<redacted>}", c.Enabled, c.APIID != 0, c.SessionFile, c.AuthMode, c.DeviceModel, c.AppVersion, c.SystemVersion, c.LangCode, c.SystemLangCode)
}

func (c Telegram) GoString() string { return c.String() }

// Plugins controls package retention and process admission. TrustFile is
// resolved beneath DataDir when relative; an empty file is valid only for
// allow-unsigned or a require-signed policy that relies on embedded keys.
type Plugins struct {
	TrustMode        string `json:"trust_mode"`
	TrustFile        string `json:"trust_file,omitempty"`
	RetainVersions   int    `json:"retain_versions"`
	MaxLoaded        int    `json:"max_loaded"`
	PythonExecutable string `json:"python_executable,omitempty"`
}

// Runtime is the complete typed core configuration owned by this package.
type Runtime struct {
	SchemaVersion   int       `json:"schema_version"`
	DataDir         string    `json:"data_dir"`
	LogLevel        string    `json:"log_level"`
	ShutdownTimeout Duration  `json:"shutdown_timeout"`
	Storage         Storage   `json:"storage"`
	Execution       Execution `json:"execution"`
	RateLimit       RateLimit `json:"rate_limit"`
	Dispatch        Dispatch  `json:"dispatch"`
	Telegram        Telegram  `json:"telegram"`
	Plugins         Plugins   `json:"plugins"`
}

// String and GoString keep configuration diagnostics useful without exposing
// credentials or the optional phone number.
func (c Runtime) String() string {
	type printable Runtime
	c.Telegram.APIHash = "<redacted>"
	c.Telegram.Phone = "<redacted>"
	return fmt.Sprintf("%+v", printable(c))
}

func (c Runtime) GoString() string {
	type printable Runtime
	c.Telegram.APIHash = "<redacted>"
	c.Telegram.Phone = "<redacted>"
	return fmt.Sprintf("%#v", printable(c))
}

func Defaults() Runtime {
	so := storage.DefaultOptions()
	return Runtime{SchemaVersion: CurrentSchemaVersion, DataDir: "data", LogLevel: "info", ShutdownTimeout: Duration(20 * time.Second), Storage: Storage{Path: "state.json", MaxNamespaces: so.MaxNamespaces, MaxKeysPerNamespace: so.MaxKeysPerNamespace, MaxTotalEntries: so.MaxTotalEntries, MaxNamespaceBytes: so.MaxNamespaceBytes, MaxKeyBytes: so.MaxKeyBytes, MaxValueBytes: so.MaxValueBytes, MaxFileBytes: so.MaxFileBytes}, Execution: Execution{MaxPlugins: 256, QueueCapacity: 128, WorkersPerPlugin: 2, HandlerTimeout: Duration(15 * time.Second)}, RateLimit: RateLimit{TokensPerSecond: 2, Burst: 30, MaxIdentities: 10_000, IdleTTL: Duration(10 * time.Minute), SweepInterval: Duration(time.Minute), Shards: 16}, Dispatch: Dispatch{CommandPrefix: ".", MaxCommands: 4096, MaxAliases: 8192, MaxWatchers: 8192}, Telegram: Telegram{SessionFile: "telegram.session.json", AuthMode: TelegramAuthPhone, DeviceModel: "Serenox", AppVersion: "0.1.0", SystemVersion: "Go", LangCode: "en", SystemLangCode: "en-US"}, Plugins: Plugins{TrustMode: "allow-unsigned", RetainVersions: 3, MaxLoaded: 32}}
}

// FieldError identifies a startup-invalid field without printing secret values.
type FieldError struct {
	Field   string
	Problem string
}

func (e *FieldError) Error() string    { return fmt.Sprintf("config: %s: %s", e.Field, e.Problem) }
func field(name, problem string) error { return &FieldError{Field: name, Problem: problem} }

func (c Runtime) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return field("schema_version", fmt.Sprintf("must be %d", CurrentSchemaVersion))
	}
	if strings.TrimSpace(c.DataDir) == "" || strings.IndexByte(c.DataDir, 0) >= 0 {
		return field("data_dir", "must be a non-empty path")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return field("log_level", "must be debug, info, warn, or error")
	}
	if c.ShutdownTimeout.Duration() < time.Second || c.ShutdownTimeout.Duration() > 10*time.Minute {
		return field("shutdown_timeout", "must be between 1s and 10m")
	}
	if strings.TrimSpace(c.Storage.Path) == "" || strings.IndexByte(c.Storage.Path, 0) >= 0 {
		return field("storage.path", "must be a non-empty path")
	}
	if _, err := c.StorageOptions(); err != nil {
		return field("storage", err.Error())
	}
	if c.Execution.MaxPlugins < 1 || c.Execution.MaxPlugins > 4096 {
		return field("execution.max_plugins", "must be between 1 and 4096")
	}
	if _, err := c.ExecutorConfig(); err != nil {
		return field("execution", err.Error())
	}
	if _, err := c.RateLimiterConfig(); err != nil {
		return field("rate_limit", err.Error())
	}
	if err := validatePrefix(c.Dispatch.CommandPrefix); err != nil {
		return field("dispatch.command_prefix", err.Error())
	}
	if c.Dispatch.MaxCommands < 1 || c.Dispatch.MaxCommands > 1_000_000 {
		return field("dispatch.max_commands", "must be between 1 and 1000000")
	}
	if c.Dispatch.MaxAliases < 1 || c.Dispatch.MaxAliases > 2_000_000 {
		return field("dispatch.max_aliases", "must be between 1 and 2000000")
	}
	if c.Dispatch.MaxWatchers < 1 || c.Dispatch.MaxWatchers > 1_000_000 {
		return field("dispatch.max_watchers", "must be between 1 and 1000000")
	}
	if err := c.validateTelegram(); err != nil {
		return err
	}
	switch c.Plugins.TrustMode {
	case "allow-unsigned", "require-signed", "require-trusted":
	default:
		return field("plugins.trust_mode", "must be allow-unsigned, require-signed, or require-trusted")
	}
	if strings.IndexByte(c.Plugins.TrustFile, 0) >= 0 {
		return field("plugins.trust_file", "must be a valid path")
	}
	if c.Plugins.RetainVersions < 2 || c.Plugins.RetainVersions > 32 {
		return field("plugins.retain_versions", "must be between 2 and 32 for activation rollback")
	}
	if c.Plugins.MaxLoaded < 1 || c.Plugins.MaxLoaded > 4096 {
		return field("plugins.max_loaded", "must be between 1 and 4096")
	}
	if strings.IndexByte(c.Plugins.PythonExecutable, 0) >= 0 {
		return field("plugins.python_executable", "must not contain NUL")
	}
	return nil
}

func (c Runtime) validateTelegram() error {
	telegram := c.Telegram
	if telegram.APIID < 0 || telegram.APIID > 2_147_483_647 {
		return field("telegram.api_id", "must be between 1 and 2147483647 when set")
	}
	if telegram.APIHash != "" {
		decoded, err := hex.DecodeString(telegram.APIHash)
		if err != nil || len(decoded) != 16 {
			return field("telegram.api_hash", "must be exactly 32 hexadecimal characters")
		}
	}
	if (telegram.APIID == 0) != (telegram.APIHash == "") {
		return field("telegram", "api_id and api_hash must be set together")
	}
	if telegram.Enabled && telegram.APIID == 0 {
		return field("telegram.api_id", "is required when Telegram transport is enabled")
	}
	if err := validateTextField("telegram.session_file", telegram.SessionFile, 4096); err != nil {
		return err
	}
	cleanSession := filepath.Clean(telegram.SessionFile)
	if cleanSession == "." {
		return field("telegram.session_file", "must name a file")
	}
	if !filepath.IsAbs(cleanSession) && (cleanSession == ".." || strings.HasPrefix(cleanSession, ".."+string(filepath.Separator))) {
		return field("telegram.session_file", "relative path must stay beneath data_dir")
	}
	if telegram.AuthMode != TelegramAuthPhone {
		return field("telegram.auth_mode", "must be phone")
	}
	for _, item := range []struct {
		name  string
		value string
		max   int
	}{
		{"telegram.device_model", telegram.DeviceModel, 128},
		{"telegram.app_version", telegram.AppVersion, 64},
		{"telegram.system_version", telegram.SystemVersion, 64},
	} {
		if err := validateTextField(item.name, item.value, item.max); err != nil {
			return err
		}
	}
	if !validLanguageCode(telegram.LangCode, false) {
		return field("telegram.lang_code", "must be a 2 to 8 character language code")
	}
	if !validLanguageCode(telegram.SystemLangCode, true) {
		return field("telegram.system_lang_code", "must be a 2 to 16 character locale code")
	}
	if telegram.Phone != "" && !validPhone(telegram.Phone) {
		return field("SERENOX_TELEGRAM_PHONE", "must use E.164 form: + followed by 7 to 15 digits")
	}
	return nil
}

func validateTextField(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || len(value) > maxBytes {
		return field(name, fmt.Sprintf("must contain 1 to %d bytes", maxBytes))
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return field(name, "must not contain control characters")
		}
	}
	return nil
}

func validLanguageCode(value string, locale bool) bool {
	if !locale {
		return asciiLetters(value, 2, 8, false)
	}
	if len(value) < 2 || len(value) > 16 {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) > 3 || !asciiLetters(parts[0], 2, 8, false) {
		return false
	}
	for _, part := range parts[1:] {
		if !(asciiLetters(part, 2, 8, true) || asciiDigits(part, 3, 3)) {
			return false
		}
	}
	return true
}

func asciiLetters(value string, minimum, maximum int, allowUpper bool) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if allowUpper && char >= 'A' && char <= 'Z' {
			continue
		}
		return false
	}
	return true
}

func asciiDigits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validPhone(value string) bool {
	if len(value) < 8 || len(value) > 16 || value[0] != '+' || value[1] == '0' {
		return false
	}
	for _, char := range value[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validatePrefix(prefix string) error {
	if prefix == "" || len(prefix) > 32 {
		return errors.New("must contain 1 to 32 bytes")
	}
	for _, r := range prefix {
		if r < 0x21 || r == 0x7f {
			return errors.New("must not contain whitespace or control characters")
		}
	}
	return nil
}

func (c Runtime) StorageOptions() (storage.Options, error) {
	opts := storage.Options{MaxNamespaces: c.Storage.MaxNamespaces, MaxKeysPerNamespace: c.Storage.MaxKeysPerNamespace, MaxTotalEntries: c.Storage.MaxTotalEntries, MaxNamespaceBytes: c.Storage.MaxNamespaceBytes, MaxKeyBytes: c.Storage.MaxKeyBytes, MaxValueBytes: c.Storage.MaxValueBytes, MaxFileBytes: c.Storage.MaxFileBytes}
	if err := opts.Validate(); err != nil {
		return storage.Options{}, err
	}
	return opts, nil
}
func (c Runtime) ExecutorConfig() (executor.Config, error) {
	cfg := executor.Config{QueueCapacity: c.Execution.QueueCapacity, Workers: c.Execution.WorkersPerPlugin, HandlerTimeout: c.Execution.HandlerTimeout.Duration()}
	if err := cfg.Validate(); err != nil {
		return executor.Config{}, err
	}
	return cfg, nil
}
func (c Runtime) RateLimiterConfig() (ratelimit.Config, error) {
	cfg := ratelimit.Config{TokensPerSecond: c.RateLimit.TokensPerSecond, Burst: c.RateLimit.Burst, MaxIdentities: c.RateLimit.MaxIdentities, IdleTTL: c.RateLimit.IdleTTL.Duration(), SweepInterval: c.RateLimit.SweepInterval.Duration(), Shards: c.RateLimit.Shards}
	if err := cfg.Validate(); err != nil {
		return ratelimit.Config{}, err
	}
	return cfg, nil
}

// StoragePath resolves a relative state path beneath DataDir.
func (c Runtime) StoragePath() string {
	if filepath.IsAbs(c.Storage.Path) {
		return filepath.Clean(c.Storage.Path)
	}
	return filepath.Join(c.DataDir, c.Storage.Path)
}

func (c Runtime) PluginTrustPath() string {
	if c.Plugins.TrustFile == "" {
		return ""
	}
	if filepath.IsAbs(c.Plugins.TrustFile) {
		return filepath.Clean(c.Plugins.TrustFile)
	}
	return filepath.Join(c.DataDir, c.Plugins.TrustFile)
}

// TelegramSessionPath resolves a relative session path beneath DataDir.
func (c Runtime) TelegramSessionPath() string {
	if filepath.IsAbs(c.Telegram.SessionFile) {
		return filepath.Clean(c.Telegram.SessionFile)
	}
	return filepath.Join(c.DataDir, c.Telegram.SessionFile)
}

type LoadOptions struct {
	Path      string
	LookupEnv func(string) (string, bool)
	// Override applies typed CLI flags after file and environment settings.
	Override func(*Runtime) error
}

func Load(path string) (Runtime, error) { return LoadWithOptions(LoadOptions{Path: path}) }

// LoadWithOptions applies defaults < JSON file < environment < typed override.
func LoadWithOptions(options LoadOptions) (Runtime, error) {
	c := Defaults()
	if options.Path != "" {
		f, err := os.Open(options.Path)
		if err != nil {
			return Runtime{}, fmt.Errorf("config: open: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		closeErr := f.Close()
		if readErr != nil {
			return Runtime{}, fmt.Errorf("config: read: %w", readErr)
		}
		if closeErr != nil {
			return Runtime{}, fmt.Errorf("config: close: %w", closeErr)
		}
		if len(data) > 1024*1024 {
			return Runtime{}, fmt.Errorf("config: file exceeds 1 MiB")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return Runtime{}, fmt.Errorf("config: decode: %w", err)
		}
		if dec.Decode(&struct{}{}) != io.EOF {
			return Runtime{}, fmt.Errorf("config: trailing JSON data")
		}
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := applyEnv(&c, lookup); err != nil {
		return Runtime{}, err
	}
	if options.Override != nil {
		if err := options.Override(&c); err != nil {
			return Runtime{}, fmt.Errorf("config: override: %w", err)
		}
	}
	if err := c.Validate(); err != nil {
		return Runtime{}, err
	}
	return c, nil
}

func applyEnv(c *Runtime, lookup func(string) (string, bool)) error {
	stringVars := map[string]*string{"SERENOX_DATA_DIR": &c.DataDir, "SERENOX_LOG_LEVEL": &c.LogLevel, "SERENOX_STORAGE_PATH": &c.Storage.Path, "SERENOX_COMMAND_PREFIX": &c.Dispatch.CommandPrefix, "SERENOX_TELEGRAM_API_HASH": &c.Telegram.APIHash, "SERENOX_TELEGRAM_SESSION_FILE": &c.Telegram.SessionFile, "SERENOX_TELEGRAM_AUTH_MODE": &c.Telegram.AuthMode, "SERENOX_TELEGRAM_DEVICE_MODEL": &c.Telegram.DeviceModel, "SERENOX_TELEGRAM_APP_VERSION": &c.Telegram.AppVersion, "SERENOX_TELEGRAM_SYSTEM_VERSION": &c.Telegram.SystemVersion, "SERENOX_TELEGRAM_LANG_CODE": &c.Telegram.LangCode, "SERENOX_TELEGRAM_SYSTEM_LANG_CODE": &c.Telegram.SystemLangCode, "SERENOX_TELEGRAM_PHONE": &c.Telegram.Phone, "SERENOX_PLUGIN_TRUST_MODE": &c.Plugins.TrustMode, "SERENOX_PLUGIN_TRUST_FILE": &c.Plugins.TrustFile, "SERENOX_PYTHON_EXECUTABLE": &c.Plugins.PythonExecutable}
	for name, target := range stringVars {
		if value, ok := lookup(name); ok {
			*target = value
		}
	}
	ints := map[string]*int{"SERENOX_MAX_PLUGINS": &c.Execution.MaxPlugins, "SERENOX_QUEUE_CAPACITY": &c.Execution.QueueCapacity, "SERENOX_WORKERS_PER_PLUGIN": &c.Execution.WorkersPerPlugin, "SERENOX_RATE_MAX_IDENTITIES": &c.RateLimit.MaxIdentities, "SERENOX_RATE_SHARDS": &c.RateLimit.Shards, "SERENOX_MAX_COMMANDS": &c.Dispatch.MaxCommands, "SERENOX_MAX_ALIASES": &c.Dispatch.MaxAliases, "SERENOX_MAX_WATCHERS": &c.Dispatch.MaxWatchers, "SERENOX_TELEGRAM_API_ID": &c.Telegram.APIID, "SERENOX_PLUGIN_RETAIN_VERSIONS": &c.Plugins.RetainVersions, "SERENOX_PLUGIN_MAX_LOADED": &c.Plugins.MaxLoaded}
	for name, target := range ints {
		if value, ok := lookup(name); ok {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return field(name, "must be an integer")
			}
			*target = parsed
		}
	}
	if value, ok := lookup("SERENOX_TELEGRAM_ENABLED"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return field("SERENOX_TELEGRAM_ENABLED", "must be a boolean")
		}
		c.Telegram.Enabled = parsed
	}
	int64s := map[string]*int64{"SERENOX_STORAGE_MAX_FILE_BYTES": &c.Storage.MaxFileBytes}
	for name, target := range int64s {
		if value, ok := lookup(name); ok {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return field(name, "must be an integer")
			}
			*target = parsed
		}
	}
	floats := map[string]*float64{"SERENOX_RATE_TOKENS_PER_SECOND": &c.RateLimit.TokensPerSecond, "SERENOX_RATE_BURST": &c.RateLimit.Burst}
	for name, target := range floats {
		if value, ok := lookup(name); ok {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return field(name, "must be a number")
			}
			*target = parsed
		}
	}
	durations := map[string]*Duration{"SERENOX_SHUTDOWN_TIMEOUT": &c.ShutdownTimeout, "SERENOX_HANDLER_TIMEOUT": &c.Execution.HandlerTimeout, "SERENOX_RATE_IDLE_TTL": &c.RateLimit.IdleTTL, "SERENOX_RATE_SWEEP_INTERVAL": &c.RateLimit.SweepInterval}
	for name, target := range durations {
		if value, ok := lookup(name); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return field(name, "must be a Go duration")
			}
			*target = Duration(parsed)
		}
	}
	return nil
}
