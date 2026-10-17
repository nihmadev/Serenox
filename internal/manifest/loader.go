package manifest

import (
	"bytes"
	"crypto/sha256"
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

	"github.com/go-faster/yaml"
	"github.com/nihmadev/Serenox/internal/config"
)

var (
	ErrUnsupportedBackend = errors.New("manifest: selected service backend is not implemented in this build")
	ErrMissingSecret      = errors.New("manifest: required secret is missing")
)

type LoadOptions struct {
	Path       string
	LegacyPath string
	LookupEnv  func(string) (string, bool)
	ReadFile   func(string) ([]byte, error)
}

type Effective struct {
	Document     Document
	Runtime      config.Runtime
	ManifestPath string
	LegacyPath   string
	Digest       string
	Source       []byte
}

func Load(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	doc, err := Decode(data)
	if err != nil {
		return Document{}, fmt.Errorf("manifest %s: %w", path, err)
	}
	return doc, nil
}

func Decode(data []byte) (Document, error) {
	if len(data) == 0 || len(data) > MaxFileBytes {
		return Document{}, fmt.Errorf("file must contain 1..%d bytes", MaxFileBytes)
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return Document{}, fmt.Errorf("decode YAML: %w", err)
	}
	if err := rejectUnsafeNodes(&root, 0, new(int)); err != nil {
		return Document{}, err
	}
	if err := validateDocumentShape(&root); err != nil {
		return Document{}, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Document{}, errors.New("multiple YAML documents are not supported")
		}
		return Document{}, fmt.Errorf("decode trailing YAML: %w", err)
	}

	var doc Document
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if err := strict.Decode(&doc); err != nil {
		return Document{}, fmt.Errorf("decode manifest fields: %w", err)
	}
	if err := validateRequiredFields(&root); err != nil {
		return Document{}, err
	}
	doc.present = collectPresence(&root)
	doc = doc.withDefaults()
	if err := doc.Validate(); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func rejectUnsafeNodes(node *yaml.Node, depth int, count *int) error {
	if node == nil {
		return errors.New("manifest: empty YAML node")
	}
	if depth > 32 {
		return errors.New("manifest: YAML nesting exceeds 32 levels")
	}
	(*count)++
	if *count > 10000 {
		return errors.New("manifest: YAML node limit exceeded")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("manifest: YAML anchors and aliases are not allowed")
	}
	if node.Kind == yaml.ScalarNode && len(node.Value) > 16*1024 {
		return errors.New("manifest: scalar exceeds 16 KiB")
	}
	if node.Kind == yaml.SequenceNode && len(node.Content) > 2048 {
		return errors.New("manifest: collection exceeds 2048 items")
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)/2 > 2048 {
			return errors.New("manifest: mapping exceeds 2048 fields")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
				return errors.New("manifest: mapping keys must be strings")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("manifest: duplicate field %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := rejectUnsafeNodes(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func validateDocumentShape(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return errors.New("manifest: document must be one mapping")
	}
	return nil
}

func validateRequiredFields(root *yaml.Node) error {
	required := []string{"apiVersion", "kind", "metadata", "runtime", "telegram", "storage", "services", "plugins", "release"}
	present := collectPresence(root)
	for _, field := range required {
		if !present[field] {
			return fmt.Errorf("manifest: required top-level field %q is missing", field)
		}
	}
	return nil
}

func collectPresence(root *yaml.Node) map[string]bool {
	result := make(map[string]bool)
	if root == nil || len(root.Content) == 0 {
		return result
	}
	var walk func(string, *yaml.Node)
	walk = func(prefix string, node *yaml.Node) {
		if node == nil || node.Kind != yaml.MappingNode {
			return
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode {
				continue
			}
			path := key.Value
			if prefix != "" {
				path = prefix + "." + path
			}
			result[path] = true
			walk(path, value)
		}
	}
	walk("", root.Content[0])
	return result
}

func (d Document) Validate() error {
	if d.APIVersion != APIVersion {
		return fmt.Errorf("manifest: apiVersion must be %q", APIVersion)
	}
	if d.Kind != Kind {
		return fmt.Errorf("manifest: kind must be %q", Kind)
	}
	if !validName(d.Metadata.Name) {
		return errors.New("manifest: metadata.name must be 1..128 lowercase URL-safe bytes")
	}
	if d.Metadata.Version < 1 || d.Metadata.Version > 1_000_000 {
		return errors.New("manifest: metadata.version must be between 1 and 1000000")
	}
	if err := d.validateRuntime(); err != nil {
		return err
	}
	if err := d.validateTelegram(); err != nil {
		return err
	}
	if err := d.validateStorage(); err != nil {
		return err
	}
	if len(d.Services) > 64 {
		return errors.New("manifest: services are limited to 64 declarations")
	}
	for name, service := range d.Services {
		if !validName(name) {
			return fmt.Errorf("manifest: invalid service name %q", name)
		}
		if err := validateService(name, service); err != nil {
			return err
		}
	}
	if len(d.Plugins) > 256 {
		return errors.New("manifest: plugins are limited to 256 references")
	}
	for index, plugin := range d.Plugins {
		if strings.TrimSpace(plugin.Path) == "" || filepath.Base(plugin.Path) != plugin.Path || !strings.HasSuffix(plugin.Path, ".srnx") {
			return fmt.Errorf("manifest: plugins[%d].path must be a local .srnx filename", index)
		}
	}
	if d.Release.RetainVersions < 2 || d.Release.RetainVersions > 32 {
		return errors.New("manifest: release.retainVersions must be between 2 and 32")
	}
	if d.Release.HealthTimeout.Duration < time.Second || d.Release.HealthTimeout.Duration > 10*time.Minute {
		return errors.New("manifest: release.healthTimeout must be between 1s and 10m")
	}
	return nil
}

func (d Document) validateRuntime() error {
	if strings.TrimSpace(d.Runtime.DataDir) == "" || filepath.IsAbs(d.Runtime.DataDir) && strings.ContainsRune(d.Runtime.DataDir, '\x00') {
		return errors.New("manifest: runtime.dataDir must be a non-empty path")
	}
	if d.Runtime.ConfigMode == "" {
		return errors.New("manifest: runtime.configMode is required")
	}
	switch d.Runtime.ConfigMode {
	case "production", "development", "recorded":
	default:
		return errors.New("manifest: runtime.configMode must be production, development, or recorded")
	}
	switch d.Runtime.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("manifest: runtime.logLevel must be debug, info, warn, or error")
	}
	if d.Runtime.ShutdownTimeout.Duration < time.Second || d.Runtime.ShutdownTimeout.Duration > 10*time.Minute {
		return errors.New("manifest: runtime.shutdownTimeout must be between 1s and 10m")
	}
	if d.Runtime.SecretDir != "" && !safeRelativePath(d.Runtime.SecretDir) {
		return errors.New("manifest: runtime.secretDir must remain beneath dataDir")
	}
	return nil
}

func (d Document) validateTelegram() error {
	if d.Telegram.SessionFile == "" || !safeRelativePath(d.Telegram.SessionFile) {
		return errors.New("manifest: telegram.sessionFile must remain beneath dataDir")
	}
	if d.Telegram.AuthMode != "" && d.Telegram.AuthMode != config.TelegramAuthPhone {
		return errors.New("manifest: telegram.authMode must be phone")
	}
	if d.Telegram.Enabled && (d.Telegram.APIHash.IsZero() || d.Telegram.APIID.IsZero()) {
		return errors.New("manifest: enabled Telegram requires apiId and apiHash references")
	}
	if !d.Telegram.APIHash.IsZero() && d.Telegram.APIHash.Env() == "" && d.Telegram.APIHash.File() == "" {
		return errors.New("manifest: telegram.apiHash must be a secret reference")
	}
	return nil
}

func (d Document) validateStorage() error {
	switch d.Storage.Backend {
	case "file":
		if d.Storage.Path == "" || !safeRelativeOrAbsolutePath(d.Storage.Path) {
			return errors.New("manifest: file storage.path must be a safe path")
		}
	case "postgres":
		if d.Storage.Service == "" {
			return errors.New("manifest: postgres storage requires storage.service")
		}
	default:
		return errors.New("manifest: storage.backend must be file or postgres")
	}
	return nil
}

func validateService(name string, service Service) error {
	switch service.Kind {
	case "postgres", "redis", "object_storage":
	default:
		return fmt.Errorf("manifest: services.%s.kind is unsupported", name)
	}
	if service.Endpoint == "" {
		return fmt.Errorf("manifest: services.%s.endpoint is required", name)
	}
	if strings.ContainsAny(service.Endpoint, "\r\n\x00") || strings.Contains(service.Endpoint, "@") {
		return fmt.Errorf("manifest: services.%s.endpoint must not contain credentials", name)
	}
	if service.TLS.Mode != "" && service.TLS.Mode != "off" && service.TLS.Mode != "required" && service.TLS.Mode != "verify" {
		return fmt.Errorf("manifest: services.%s.tls.mode is invalid", name)
	}
	if service.MaxConnections < 0 || service.MaxConnections > 1024 || service.MaxRetries < 0 || service.MaxRetries > 10 || service.MaxBytes < 0 || service.MaxBytes > 1<<40 || service.MaxRequests < 0 || service.MaxRequests > 1_000_000 {
		return fmt.Errorf("manifest: services.%s budgets exceed supported bounds", name)
	}
	if service.Kind == "object_storage" && (service.Bucket == "" || !validObjectName(service.Bucket)) {
		return fmt.Errorf("manifest: services.%s.bucket is required and must be safe", name)
	}
	return nil
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
		if index == 0 && (char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func validObjectName(value string) bool {
	if len(value) == 0 || len(value) > 63 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func safeRelativePath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func safeRelativeOrAbsolutePath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return false
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value) != string(filepath.Separator)
	}
	return safeRelativePath(value)
}

func (d Document) withDefaults() Document {
	if d.Runtime.DataDir == "" {
		d.Runtime.DataDir = "data"
	}
	if d.Runtime.ConfigMode == "" {
		d.Runtime.ConfigMode = "production"
	}
	if d.Runtime.LogLevel == "" {
		d.Runtime.LogLevel = "info"
	}
	if d.Runtime.ShutdownTimeout.Duration == 0 {
		d.Runtime.ShutdownTimeout.Duration = 20 * time.Second
	}
	if d.Runtime.SecretDir == "" {
		d.Runtime.SecretDir = "secrets"
	}
	if d.Telegram.SessionFile == "" {
		d.Telegram.SessionFile = "telegram.session.json"
	}
	if d.Telegram.AuthMode == "" {
		d.Telegram.AuthMode = config.TelegramAuthPhone
	}
	if d.Storage.Backend == "" {
		d.Storage.Backend = "file"
	}
	if d.Storage.Path == "" && d.Storage.Backend == "file" {
		d.Storage.Path = "state.json"
	}
	if d.Services == nil {
		d.Services = map[string]Service{}
	}
	if d.Plugins == nil {
		d.Plugins = []PluginRef{}
	}
	if d.Release.RetainVersions == 0 {
		d.Release.RetainVersions = 3
	}
	if d.Release.HealthTimeout.Duration == 0 {
		d.Release.HealthTimeout.Duration = 30 * time.Second
	}
	return d
}

func (d Document) CanonicalJSON() ([]byte, error) {
	copy := d
	copy.present = nil
	return json.Marshal(copy)
}

func (d Document) Digest() (string, error) {
	data, err := d.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (d Document) runtimeField(name string) bool {
	return d.present == nil || d.present[name]
}

func parseIntInput(input IntInput, resolve func(SecretRef) (string, error)) (int, error) {
	value := input.Value
	if value == "" && !input.Ref.IsZero() {
		var err error
		value, err = resolve(input.Ref)
		if err != nil {
			return 0, err
		}
	}
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("Telegram apiId must be a positive integer")
	}
	return parsed, nil
}
