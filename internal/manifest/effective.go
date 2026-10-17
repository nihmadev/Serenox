package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/config"
)

type SecretResolver struct {
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	BaseDir   string
	SecretDir string
}

func (r SecretResolver) Resolve(ref SecretRef) (string, error) {
	if ref.Env() != "" {
		lookup := r.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		value, ok := lookup(ref.Env())
		if !ok || value == "" {
			return "", fmt.Errorf("%w: environment variable %s", ErrMissingSecret, ref.Env())
		}
		return validateSecretValue(value)
	}
	if ref.File() == "" {
		return "", errors.New("manifest: empty secret reference")
	}
	root := r.SecretDir
	if root == "" {
		root = filepath.Join(r.BaseDir, "secrets")
	}
	path, err := secureSecretPath(root, ref.File())
	if err != nil {
		return "", err
	}
	readFile := r.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrMissingSecret, filepath.Base(path))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("manifest: secret file %q must be a regular mode-0600 file", ref.File())
	}
	data, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	if len(data) > 64*1024 {
		return "", errors.New("manifest: secret file exceeds 64 KiB")
	}
	return validateSecretValue(strings.TrimSpace(string(data)))
}

func validateSecretValue(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("manifest: secret value is empty or contains control characters")
	}
	return value, nil
}

func secureSecretPath(root, requested string) (string, error) {
	if filepath.IsAbs(requested) || strings.ContainsRune(requested, '\x00') {
		return "", errors.New("manifest: secret file path must be relative")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve secret directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(root, requested))
	if err != nil {
		return "", fmt.Errorf("resolve secret file: %w", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("manifest: secret file path escapes the allowed directory")
	}
	return path, nil
}

func (e Effective) RedactedJSON() ([]byte, error) {
	runtime := e.Runtime
	runtime.Telegram.APIHash = "<redacted>"
	runtime.Telegram.Phone = "<redacted>"
	type output struct {
		Manifest Document       `json:"manifest"`
		Runtime  config.Runtime `json:"runtime"`
		Digest   string         `json:"manifest_digest"`
	}
	return marshalIndented(output{Manifest: e.Document, Runtime: runtime, Digest: e.Digest})
}

func marshalIndented(value any) ([]byte, error) {
	data, err := jsonMarshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// jsonMarshal is kept behind a small function so output tests can replace it
// in future without making the manifest loader depend on logging code.
var jsonMarshal = func(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func LoadEffective(options LoadOptions) (Effective, error) {
	if options.Path == "" {
		return Effective{}, errors.New("manifest: path is required")
	}
	source, err := os.ReadFile(options.Path)
	if options.ReadFile != nil {
		source, err = options.ReadFile(options.Path)
	}
	if err != nil {
		return Effective{}, fmt.Errorf("manifest: read %s: %w", options.Path, err)
	}
	doc, err := Decode(source)
	if err != nil {
		return Effective{}, fmt.Errorf("manifest %s: %w", options.Path, err)
	}
	base := config.Defaults()
	if options.LegacyPath != "" {
		base, err = config.LoadWithOptions(config.LoadOptions{
			Path: options.LegacyPath, LookupEnv: func(string) (string, bool) { return "", false },
			AllowIncompleteTelegramCredentials: true,
		})
		if err != nil {
			return Effective{}, fmt.Errorf("manifest: load legacy config: %w", err)
		}
	}
	resolver := SecretResolver{LookupEnv: options.LookupEnv, ReadFile: options.ReadFile}
	runtimeConfig, err := doc.toRuntime(base, resolver, options.Path)
	if err != nil {
		return Effective{}, err
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := config.ApplyEnvironmentOverrides(&runtimeConfig, lookup); err != nil {
		return Effective{}, fmt.Errorf("manifest: environment override: %w", err)
	}
	if err := runtimeConfig.Validate(); err != nil {
		return Effective{}, fmt.Errorf("manifest: effective configuration: %w", err)
	}
	digest, err := doc.Digest()
	if err != nil {
		return Effective{}, fmt.Errorf("manifest: digest: %w", err)
	}
	return Effective{Document: doc, Runtime: runtimeConfig, ManifestPath: options.Path, LegacyPath: options.LegacyPath, Digest: digest, Source: append([]byte(nil), source...)}, nil
}

func (d Document) toRuntime(base config.Runtime, resolver SecretResolver, manifestPath string) (config.Runtime, error) {
	raw := d
	d = d.withDefaults()
	result := base
	set := func(path string) bool { return d.runtimeField(path) }
	manifestDir, _ := filepath.Abs(filepath.Dir(manifestPath))
	if set("runtime.dataDir") && !filepath.IsAbs(d.Runtime.DataDir) {
		result.DataDir = filepath.Join(manifestDir, d.Runtime.DataDir)
	}
	secretRoot := d.Runtime.SecretDir
	if !filepath.IsAbs(secretRoot) {
		secretRoot = filepath.Join(result.DataDir, secretRoot)
	}
	resolver.SecretDir = secretRoot
	resolver.BaseDir = result.DataDir
	if set("runtime.logLevel") {
		result.LogLevel = d.Runtime.LogLevel
	}
	if set("runtime.shutdownTimeout") {
		result.ShutdownTimeout = config.Duration(d.Runtime.ShutdownTimeout.Duration)
	}
	if set("runtime.queueCapacity") {
		result.Execution.QueueCapacity = d.Runtime.QueueCapacity
	}
	if set("runtime.workers") {
		result.Execution.WorkersPerPlugin = d.Runtime.Workers
	}
	if set("runtime.handlerTimeout") {
		result.Execution.HandlerTimeout = config.Duration(d.Runtime.HandlerTimeout.Duration)
	}
	if set("telegram.enabled") {
		result.Telegram.Enabled = d.Telegram.Enabled
	}
	if set("telegram.sessionFile") {
		result.Telegram.SessionFile = d.Telegram.SessionFile
	}
	if set("telegram.authMode") {
		result.Telegram.AuthMode = d.Telegram.AuthMode
	}
	if !d.Telegram.APIID.IsZero() {
		apiIDInput := d.Telegram.APIID
		if value, ok := lookupValue(resolver.LookupEnv, "SERENOX_TELEGRAM_API_ID"); ok {
			apiIDInput = IntInput{Value: value}
		}
		apiID, err := parseIntInput(apiIDInput, resolver.Resolve)
		if err != nil {
			return config.Runtime{}, fmt.Errorf("manifest: telegram.apiId: %w", err)
		}
		result.Telegram.APIID = apiID
	}
	if !d.Telegram.APIHash.IsZero() {
		apiHash := ""
		var err error
		if value, ok := lookupValue(resolver.LookupEnv, "SERENOX_TELEGRAM_API_HASH"); ok {
			apiHash = value
		} else {
			apiHash, err = resolver.Resolve(d.Telegram.APIHash)
		}
		if err != nil {
			return config.Runtime{}, fmt.Errorf("manifest: telegram.apiHash: %w", err)
		}
		result.Telegram.APIHash = apiHash
	}
	if d.Storage.Backend == "postgres" {
		result.Storage.Path = d.Storage.Service
	} else if raw.present == nil || raw.present["storage.path"] || raw.present["storage.backend"] {
		result.Storage.Path = d.Storage.Path
	}
	result.Storage.MaxNamespaces = nonZero(d.Storage.MaxNamespaces, result.Storage.MaxNamespaces)
	result.Storage.MaxKeysPerNamespace = nonZero(d.Storage.MaxKeysPerNamespace, result.Storage.MaxKeysPerNamespace)
	result.Storage.MaxTotalEntries = nonZero(d.Storage.MaxTotalEntries, result.Storage.MaxTotalEntries)
	result.Storage.MaxNamespaceBytes = nonZero(d.Storage.MaxNamespaceBytes, result.Storage.MaxNamespaceBytes)
	result.Storage.MaxKeyBytes = nonZero(d.Storage.MaxKeyBytes, result.Storage.MaxKeyBytes)
	result.Storage.MaxValueBytes = nonZero(d.Storage.MaxValueBytes, result.Storage.MaxValueBytes)
	if d.Storage.MaxFileBytes != 0 {
		result.Storage.MaxFileBytes = d.Storage.MaxFileBytes
	}
	if set("runtime.dataDir") && !filepath.IsAbs(result.DataDir) {
		result.DataDir = filepath.Join(manifestDir, result.DataDir)
	}
	if !filepath.IsAbs(result.Storage.Path) && d.Storage.Backend == "file" {
		// config.Runtime resolves the path beneath DataDir at use time.
		result.Storage.Path = filepath.Clean(result.Storage.Path)
	}
	return result, nil
}

func lookupValue(lookup func(string) (string, bool), name string) (string, bool) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, ok := lookup(name)
	return value, ok && value != ""
}

func nonZero(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func (e Effective) BackendSupported() error {
	if e.Document.Storage.Backend != "file" {
		return fmt.Errorf("%w: storage.backend=%s (PostgreSQL adapter is not enabled in this build)", ErrUnsupportedBackend, e.Document.Storage.Backend)
	}
	return nil
}

func Discover(directory string) (string, bool, error) {
	manifestPath := filepath.Join(directory, "serenox.yaml")
	legacyPath := filepath.Join(directory, "config.json")
	_, manifestErr := os.Stat(manifestPath)
	_, legacyErr := os.Stat(legacyPath)
	if manifestErr == nil && errors.Is(legacyErr, os.ErrNotExist) {
		return manifestPath, true, nil
	}
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return "", false, manifestErr
	}
	return "", false, nil
}

func ParseDuration(value string) (time.Duration, error) { return time.ParseDuration(value) }
