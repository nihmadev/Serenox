package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliManifest = `apiVersion: serenox.app/v1
kind: Application
metadata: {name: cli-test, version: 1}
runtime: {dataDir: data, configMode: recorded, logLevel: info, shutdownTimeout: 20s}
telegram: {enabled: false, sessionFile: telegram.session.json}
storage: {backend: file, path: state.json}
services: {}
plugins: []
release: {retainVersions: 3, healthTimeout: 30s}
`

func TestManifestAndEffectiveCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serenox.yaml")
	if err := os.WriteFile(path, []byte(cliManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Execute(context.Background(), []string{"manifest", "check", "--file", path}, strings.NewReader(""), &stdout, &stderr, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "manifest OK") || stderr.Len() != 0 {
		t.Fatalf("check stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := Execute(context.Background(), []string{"config", "effective", "--manifest", path}, strings.NewReader(""), &stdout, &stderr, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"manifest_digest"`) || strings.Contains(stdout.String(), "telegram.session") == false {
		t.Fatalf("effective output = %s", stdout.String())
	}
}
