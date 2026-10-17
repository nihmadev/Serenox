package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testYAML = `apiVersion: serenox.app/v1
kind: Application
metadata:
  name: personal-serenox
  version: 1
runtime:
  dataDir: data
  configMode: recorded
  logLevel: info
  shutdownTimeout: 20s
telegram:
  enabled: true
  apiId: ${SERENOX_TELEGRAM_API_ID}
  apiHash: ${SERENOX_TELEGRAM_API_HASH}
  sessionFile: telegram.session.json
storage:
  backend: file
  path: state.json
services: {}
plugins: []
release:
  retainVersions: 3
  healthTimeout: 30s
`

func TestDecodeStrictAndBounded(t *testing.T) {
	doc, err := Decode([]byte(testYAML))
	if err != nil {
		t.Fatal(err)
	}
	if doc.APIVersion != APIVersion || doc.Storage.Backend != "file" || doc.Release.RetainVersions != 3 {
		t.Fatalf("decoded = %#v", doc)
	}
	for name, input := range map[string]string{
		"unknown":          strings.Replace(testYAML, "services: {}", "services: {}\nunknown: true", 1),
		"duplicate":        strings.Replace(testYAML, "kind: Application", "kind: Application\nkind: Application", 1),
		"anchor":           strings.Replace(testYAML, "dataDir: data", "dataDir: &root data", 1),
		"numeric duration": strings.Replace(testYAML, "shutdownTimeout: 20s", "shutdownTimeout: 20", 1),
	} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Errorf("%s input accepted", name)
		}
	}
}

func TestEffectiveResolvesAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serenox.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	effective, err := LoadEffective(LoadOptions{Path: path, LookupEnv: func(name string) (string, bool) {
		return map[string]string{"SERENOX_TELEGRAM_API_ID": "12345", "SERENOX_TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef"}[name], name == "SERENOX_TELEGRAM_API_ID" || name == "SERENOX_TELEGRAM_API_HASH"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if effective.Runtime.Telegram.APIID != 12345 || effective.Runtime.Telegram.APIHash == "" {
		t.Fatalf("runtime = %#v", effective.Runtime.Telegram)
	}
	data, err := effective.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "0123456789abcdef") || !strings.Contains(string(data), "<redacted>") {
		t.Fatalf("redaction failed: %s", data)
	}
	if effective.Runtime.DataDir != filepath.Join(dir, "data") {
		t.Fatalf("data dir = %q", effective.Runtime.DataDir)
	}
}

func TestSecretFileRequiresMode0600AndStaysInRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "secret")
	if err := os.WriteFile(path, []byte("value"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := ParseSecretRef("${SERENOX_SECRET}")
	if err != nil {
		t.Fatal(err)
	}
	fileRef := SecretRef{file: "secret"}
	if _, err := (SecretResolver{BaseDir: root, SecretDir: root}).Resolve(fileRef); err == nil {
		t.Fatal("insecure secret file accepted")
	}
	if _, err := (SecretResolver{BaseDir: root, SecretDir: root}).Resolve(ref); err == nil {
		t.Fatal("missing environment secret accepted")
	}
	if _, err := secureSecretPath(root, "../secret"); err == nil {
		t.Fatal("secret traversal accepted")
	}
}

func TestBackupRoundTripAndChecksumValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serenox.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	effective, err := LoadEffective(LoadOptions{Path: path, LookupEnv: func(name string) (string, bool) {
		values := map[string]string{"SERENOX_TELEGRAM_API_ID": "12345", "SERENOX_TELEGRAM_API_HASH": "0123456789abcdef0123456789abcdef"}
		value, ok := values[name]
		return value, ok
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(effective.Runtime.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := effective.Runtime.StoragePath()
	state := []byte(`{"schema_version":1,"namespaces":{}}`)
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := CreateBackup(effective, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(backup, 64<<20); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 16 {
		t.Fatal("backup is unexpectedly small")
	}
	data[len(data)-1] ^= 1
	corrupt := filepath.Join(dir, "corrupt.srb")
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(corrupt, 64<<20); err == nil {
		t.Fatal("corrupt backup accepted")
	}
}

func TestLegacyMigrationIsNonDestructiveAndUsesSecretReferences(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "config.json")
	to := filepath.Join(dir, "serenox.yaml")
	legacy := `{"schema_version":1,"data_dir":"data","telegram":{"enabled":true,"api_id":12345,"api_hash":"0123456789abcdef0123456789abcdef","session_file":"telegram.session.json","auth_mode":"phone","device_model":"Serenox","app_version":"0.1.0","system_version":"Go","lang_code":"en","system_lang_code":"en-US"}}`
	if err := os.WriteFile(from, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateLegacy(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) == 0 || len(report.SecretSources) != 1 {
		t.Fatalf("migration report = %#v", report)
	}
	data, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "0123456789abcdef") || !strings.Contains(string(data), "SERENOX_TELEGRAM_API_HASH") {
		t.Fatalf("migration leaked or omitted secret ref: %s", data)
	}
	if _, err := MigrateLegacy(from, to); err == nil {
		t.Fatal("migration overwrote existing destination")
	}
}
