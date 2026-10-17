package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-faster/yaml"
	"github.com/nihmadev/Serenox/internal/config"
)

type MigrationReport struct {
	From          string   `json:"from"`
	To            string   `json:"to"`
	Warnings      []string `json:"warnings,omitempty"`
	SecretSources []string `json:"secret_sources"`
}

func MigrateLegacy(from, to string) (MigrationReport, error) {
	if from == "" || to == "" {
		return MigrationReport{}, errors.New("migration requires --from and --to")
	}
	legacy, err := config.LoadWithOptions(config.LoadOptions{Path: from, AllowIncompleteTelegramCredentials: true, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		return MigrationReport{}, fmt.Errorf("migration: load legacy JSON: %w", err)
	}
	if _, err := os.Stat(to); err == nil {
		return MigrationReport{}, fmt.Errorf("migration: refusing to overwrite %s", to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return MigrationReport{}, err
	}
	doc := fromRuntime(legacy)
	data, err := yaml.Marshal(doc)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("migration: encode YAML: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return MigrationReport{}, err
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		return MigrationReport{}, err
	}
	report := MigrationReport{From: from, To: to, SecretSources: []string{"SERENOX_TELEGRAM_API_HASH"}}
	if legacy.Telegram.APIHash != "" {
		report.Warnings = append(report.Warnings, "telegram.apiHash was not copied; provide SERENOX_TELEGRAM_API_HASH")
	}
	if legacy.Telegram.Phone != "" {
		report.Warnings = append(report.Warnings, "Telegram phone was not copied; provide SERENOX_TELEGRAM_PHONE for interactive login")
	}
	return report, nil
}

func fromRuntime(runtime config.Runtime) Document {
	apiID := IntInput{Value: strconv.Itoa(runtime.Telegram.APIID)}
	if runtime.Telegram.APIID == 0 {
		apiID = IntInput{Ref: mustSecret("SERENOX_TELEGRAM_API_ID")}
	}
	doc := Document{
		APIVersion: APIVersion, Kind: Kind,
		Metadata: Metadata{Name: "serenox-migrated", Version: 1},
		Runtime:  Runtime{DataDir: runtime.DataDir, ConfigMode: "production", LogLevel: runtime.LogLevel, ShutdownTimeout: Duration{Duration: runtime.ShutdownTimeout.Duration()}, SecretDir: "secrets"},
		Telegram: Telegram{Enabled: runtime.Telegram.Enabled, APIID: apiID, APIHash: mustSecret("SERENOX_TELEGRAM_API_HASH"), SessionFile: runtime.Telegram.SessionFile, AuthMode: runtime.Telegram.AuthMode},
		Storage:  Storage{Backend: "file", Path: runtime.Storage.Path, MaxNamespaces: runtime.Storage.MaxNamespaces, MaxKeysPerNamespace: runtime.Storage.MaxKeysPerNamespace, MaxTotalEntries: runtime.Storage.MaxTotalEntries, MaxNamespaceBytes: runtime.Storage.MaxNamespaceBytes, MaxKeyBytes: runtime.Storage.MaxKeyBytes, MaxValueBytes: runtime.Storage.MaxValueBytes, MaxFileBytes: runtime.Storage.MaxFileBytes},
		Services: map[string]Service{}, Plugins: []PluginRef{}, Release: Release{RetainVersions: runtime.Plugins.RetainVersions, HealthTimeout: Duration{Duration: runtime.ShutdownTimeout.Duration()}},
	}
	return doc
}

func mustSecret(name string) SecretRef {
	ref, _ := ParseSecretRef("${" + name + "}")
	return ref
}

func (r MigrationReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func (d Document) String() string {
	data, err := yaml.Marshal(d)
	if err != nil {
		return "manifest: <invalid>"
	}
	return strings.TrimSpace(string(data))
}
