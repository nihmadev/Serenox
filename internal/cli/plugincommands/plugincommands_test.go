package plugincommands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/daemon"
	"github.com/nihmadev/Serenox/internal/migrationkit"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

type installHotLoadRecorder struct {
	loaded chan string
}

func (r installHotLoadRecorder) Load(_ context.Context, packagePath string) error {
	r.loaded <- packagePath
	return nil
}

func TestCLIEndToEndPackageLifecycle(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "one")
	packageOne := filepath.Join(t.TempDir(), "plugin.data")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"pack", "--source", source, "--output", packageOne}, stdout, stderr); err != nil {
		t.Fatalf("pack: %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout.String(), "Package packed") || !strings.Contains(stdout.String(), "Plugin:") || !strings.Contains(stdout.String(), "cli.example") || !strings.Contains(stdout.String(), "Version:") || !strings.Contains(stdout.String(), "1.0.0") {
		t.Fatalf("pack output = %q", stdout)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"verify", packageOne}, stdout, stderr); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(stdout.String(), "Package verified") || !strings.Contains(stdout.String(), "Plugin:") || !strings.Contains(stdout.String(), "cli.example") || !strings.Contains(stdout.String(), "Version:") || !strings.Contains(stdout.String(), "1.0.0") {
		t.Fatalf("verify output = %q", stdout)
	}

	root := t.TempDir()
	stdout.Reset()
	if err := Run(context.Background(), []string{"install", "--root", root, packageOne}, stdout, stderr); err != nil {
		t.Fatalf("install(v1): %v", err)
	}
	writeCLISource(t, source, "2.0.0", "two")
	packageTwo := filepath.Join(t.TempDir(), "plugin.srnx")
	if err := Run(context.Background(), []string{"pack", "--source", source, "--output", packageTwo}, stdout, stderr); err != nil {
		t.Fatalf("pack(v2): %v", err)
	}
	if err := Run(context.Background(), []string{"install", "--root", root, packageTwo}, stdout, stderr); err != nil {
		t.Fatalf("install(v2): %v", err)
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"list", "--root", root}, stdout, stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout.String(), "Installed plugins (1)") ||
		!strings.Contains(stdout.String(), "  Versions:") || !strings.Contains(stdout.String(), "Digest:") {
		t.Fatalf("human list output = %q", stdout)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"list", "--root", root, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout.String(), `"active_version": "2.0.0"`) {
		t.Fatalf("list output = %q", stdout)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"rollback", "--root", root, "cli.example"}, stdout, stderr); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(stdout.String(), "Plugin activated") || !strings.Contains(stdout.String(), "Version:") || !strings.Contains(stdout.String(), "1.0.0") {
		t.Fatalf("rollback output = %q", stdout)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"uninstall", "--root", root, "cli.example"}, stdout, stderr); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(stdout.String(), "Plugin uninstalled") || !strings.Contains(stdout.String(), "Plugin:") || !strings.Contains(stdout.String(), "cli.example") {
		t.Fatalf("uninstall output = %q", stdout)
	}
}

func TestCLIInstallHotLoadsRunningDaemon(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "hot-load")
	packagePath := filepath.Join(t.TempDir(), "plugin.srnx")
	if err := Run(context.Background(), []string{"pack", "--source", source, "--output", packagePath}, new(bytes.Buffer), new(bytes.Buffer)); err != nil {
		t.Fatalf("pack: %v", err)
	}
	root := t.TempDir()
	recorder := installHotLoadRecorder{loaded: make(chan string, 1)}
	server, err := daemon.NewControlServer(root, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	stdout := new(bytes.Buffer)
	if err := Run(context.Background(), []string{"install", "--root", root, packagePath}, stdout, new(bytes.Buffer)); err != nil {
		t.Fatalf("install: %v; stdout=%s", err, stdout)
	}
	select {
	case loadedPath := <-recorder.loaded:
		absolute, _ := filepath.Abs(packagePath)
		if loadedPath != absolute {
			t.Fatalf("hot-loaded path = %q, want %q", loadedPath, absolute)
		}
	case <-time.After(time.Second):
		t.Fatal("install did not request daemon hot-load")
	}
	if !strings.Contains(stdout.String(), "Plugin hot-loaded") {
		t.Fatalf("install output = %q", stdout)
	}
}

func TestCLIRequiresContentVerificationRegardlessOfExtension(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "looks-valid.srnx")
	if err := os.WriteFile(filename, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"verify", filename}, new(bytes.Buffer), new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "ZIP") {
		t.Fatalf("verify error = %v", err)
	}
}

func TestCLIPackInfersStableOutputName(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "one")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"pack", "--source", source}, stdout, stderr); err != nil {
		t.Fatalf("pack: %v; stderr=%s", err, stderr)
	}
	expected := filepath.Join(filepath.Dir(source), "dist", "cli.example-1.0.0.srnx")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("inferred package %q: %v; stdout=%q", expected, err, stdout)
	}
	if !strings.Contains(stdout.String(), "Archive:") || !strings.Contains(stdout.String(), expected) {
		t.Fatalf("pack output = %q", stdout)
	}
}

func TestCLIBuildCompilesEntrypointAndPacks(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "placeholder")
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.local/cli\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"build", "--source", source}, stdout, stderr); err != nil {
		t.Fatalf("build: %v; stderr=%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(source, "bin", "plugin")); err != nil {
		t.Fatalf("built entrypoint: %v", err)
	}
	archive := filepath.Join(filepath.Dir(source), "dist", "cli.example-1.0.0.srnx")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("built archive: %v; stdout=%q", err, stdout)
	}
	if !strings.Contains(stdout.String(), "Plugin built") || !strings.Contains(stdout.String(), "Package packed") {
		t.Fatalf("build output = %q", stdout)
	}
}

func TestCLIInstallPacksSourceDirectory(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "source")
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.local/install\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"install", "--root", root, source}, stdout, stderr); err != nil {
		t.Fatalf("install source: %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout.String(), "Plugin built") || !strings.Contains(stdout.String(), "Plugin:") || !strings.Contains(stdout.String(), "cli.example") ||
		!strings.Contains(stdout.String(), "Plugin installed") || !strings.Contains(stdout.String(), "Status:") || !strings.Contains(stdout.String(), "active") {
		t.Fatalf("install output = %q", stdout)
	}
}

func TestCLIInstallWithoutInputUsesCurrentPluginDirectory(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "source")
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.local/install-current\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Chdir(source)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"install", "--root", root}, stdout, stderr); err != nil {
		t.Fatalf("install current directory: %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout.String(), "Plugin installed") || !strings.Contains(stdout.String(), "Root:") {
		t.Fatalf("install output = %q", stdout)
	}
}

func TestCLIInstallInputErrorsExplainTheNextCommand(t *testing.T) {
	for _, input := range []string{"pack", "time"} {
		t.Run(input, func(t *testing.T) {
			err := Run(context.Background(), []string{"install", input}, new(bytes.Buffer), new(bytes.Buffer))
			if err == nil || !strings.Contains(err.Error(), "serenox plugin install ./my-plugin") ||
				!strings.Contains(err.Error(), "discovers the project") {
				t.Fatalf("install %q error = %v", input, err)
			}
		})
	}
}

func TestCLIPackMissingEntrypointSuggestsBuild(t *testing.T) {
	source := t.TempDir()
	writeCLISource(t, source, "1.0.0", "source")
	if err := os.Remove(filepath.Join(source, "bin", "plugin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"pack", "--source", source}, new(bytes.Buffer), new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "manifest expects a built entrypoint") ||
		!strings.Contains(err.Error(), "serenox plugin build") {
		t.Fatalf("pack missing entrypoint error = %v", err)
	}
}

func TestCLIMigrationKitInitAndPreflight(t *testing.T) {
	source := filepath.Join(t.TempDir(), "my-uwu")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if err := Run(context.Background(), []string{"init", "--template", "uwu", "--output", source}, stdout, stderr); err != nil {
		t.Fatalf("init: %v; stderr=%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(source, "plugin.py")); err != nil {
		t.Fatalf("starter plugin: %v", err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"preflight", "--source", source}, stdout, stderr); err != nil {
		t.Fatalf("preflight: %v; stdout=%s; stderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout.String(), "Migration preflight PASS") ||
		!strings.Contains(stdout.String(), "commands") || !strings.Contains(stdout.String(), "pre_send") ||
		!strings.Contains(stdout.String(), "telegram.reply") {
		t.Fatalf("preflight output = %q", stdout)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"preflight", "--source", source, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("json preflight: %v", err)
	}
	var report struct {
		PackageValid bool `json:"package_valid"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("preflight JSON: %v; output=%s", err, stdout)
	}
	if !report.PackageValid {
		t.Fatal("preflight JSON reported an invalid package")
	}

	legacySource := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacySource, "manifest.json"), []byte(`{"format":"serenox.plugin.package/v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySource, "plugin.py"), []byte("from pyrogram import Client\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	err := Run(context.Background(), []string{"preflight", "--source", legacySource}, stdout, stderr)
	if err == nil || !strings.Contains(stdout.String(), "raw-client") {
		t.Fatalf("legacy preflight err=%v output=%q", err, stdout)
	}
}

func TestMigrationKitScaffoldRefusesNonEmptyDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := migrationkit.Scaffold(directory, "uwu"); err == nil {
		t.Fatal("scaffold overwrote a non-empty directory")
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
