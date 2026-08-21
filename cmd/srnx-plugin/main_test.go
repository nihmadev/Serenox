package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

func TestCLIEndToEndPackageLifecycle(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "one")
	packageOne := filepath.Join(t.TempDir(), "plugin.data")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := run(context.Background(), []string{"pack", "--source", source, "--output", packageOne}, stdout, stderr); err != nil {
		t.Fatalf("pack: %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout.String(), "packed cli.example@1.0.0") {
		t.Fatalf("pack output = %q", stdout)
	}
	stdout.Reset()
	if err := run(context.Background(), []string{"verify", packageOne}, stdout, stderr); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(stdout.String(), "verified cli.example@1.0.0") {
		t.Fatalf("verify output = %q", stdout)
	}

	root := t.TempDir()
	stdout.Reset()
	if err := run(context.Background(), []string{"install", "--root", root, packageOne}, stdout, stderr); err != nil {
		t.Fatalf("install(v1): %v", err)
	}
	writeCLISource(t, source, "2.0.0", "two")
	packageTwo := filepath.Join(t.TempDir(), "plugin.srnx")
	if err := run(context.Background(), []string{"pack", "--source", source, "--output", packageTwo}, stdout, stderr); err != nil {
		t.Fatalf("pack(v2): %v", err)
	}
	if err := run(context.Background(), []string{"install", "--root", root, packageTwo}, stdout, stderr); err != nil {
		t.Fatalf("install(v2): %v", err)
	}

	stdout.Reset()
	if err := run(context.Background(), []string{"list", "--root", root, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout.String(), `"active_version": "2.0.0"`) {
		t.Fatalf("list output = %q", stdout)
	}
	stdout.Reset()
	if err := run(context.Background(), []string{"rollback", "--root", root, "cli.example"}, stdout, stderr); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(stdout.String(), "activated cli.example@1.0.0") {
		t.Fatalf("rollback output = %q", stdout)
	}
	stdout.Reset()
	if err := run(context.Background(), []string{"uninstall", "--root", root, "cli.example"}, stdout, stderr); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(stdout.String(), "uninstalled cli.example") {
		t.Fatalf("uninstall output = %q", stdout)
	}
}

func TestCLIRequiresContentVerificationRegardlessOfExtension(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "looks-valid.srnx")
	if err := os.WriteFile(filename, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"verify", filename}, new(bytes.Buffer), new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "ZIP") {
		t.Fatalf("verify error = %v", err)
	}
}

func writeCLISource(t *testing.T, source, version, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "plugin"), []byte(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := pluginpackage.Manifest{
		Format: pluginpackage.FormatV1, ID: "cli.example", Name: "CLI example", Version: version,
		Category:     pluginpackage.CategoryGoExecutable,
		Protocol:     pluginpackage.VersionRange{Min: "1.0", MaxExclusive: "2.0"},
		Core:         pluginpackage.VersionRange{Min: "0.1.0", MaxExclusive: "1.0.0"},
		Entrypoints:  []pluginpackage.Entrypoint{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "bin/plugin"}},
		Capabilities: []string{"commands"}, Permissions: []string{"telegram.reply"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, pluginpackage.ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
