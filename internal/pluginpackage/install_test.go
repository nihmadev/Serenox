package pluginpackage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInstallListRollbackAndUninstall(t *testing.T) {
	packageOne := buildTestPackage(t, "1.0.0", []byte("one"))
	packageTwo := buildTestPackage(t, "2.0.0", []byte("two"))
	manager := newTestManager(t, t.TempDir(), 3)

	first, err := manager.Install(context.Background(), packageOne)
	if err != nil {
		t.Fatalf("Install(v1): %v", err)
	}
	second, err := manager.Install(context.Background(), packageTwo)
	if err != nil {
		t.Fatalf("Install(v2): %v", err)
	}
	if first.Version != "1.0.0" || second.Version != "2.0.0" {
		t.Fatalf("receipts: %+v %+v", first, second)
	}
	if len(first.Capabilities) != 1 || first.Capabilities[0] != "commands" ||
		len(first.Permissions) != 1 || first.Permissions[0] != "telegram.reply" {
		t.Fatalf("receipt did not preserve manifest declarations: %+v", first)
	}
	plugins, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].ActiveVersion != "2.0.0" || len(plugins[0].Versions) != 2 {
		t.Fatalf("unexpected list after install: %+v", plugins)
	}
	rolledBack, err := manager.Rollback(context.Background(), "example.hello", "")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolledBack.Version != "1.0.0" {
		t.Fatalf("rollback selected %q", rolledBack.Version)
	}
	plugins, err = manager.List(context.Background())
	if err != nil || plugins[0].ActiveVersion != "1.0.0" {
		t.Fatalf("active after rollback: %+v, %v", plugins, err)
	}
	if err := manager.Uninstall(context.Background(), "example.hello", "1.0.0"); err != nil {
		t.Fatalf("Uninstall(active v1): %v", err)
	}
	plugins, err = manager.List(context.Background())
	if err != nil || len(plugins) != 1 || plugins[0].ActiveVersion != "2.0.0" || len(plugins[0].Versions) != 1 {
		t.Fatalf("active after version uninstall: %+v, %v", plugins, err)
	}
	if err := manager.Uninstall(context.Background(), "example.hello", ""); err != nil {
		t.Fatalf("Uninstall(all): %v", err)
	}
	plugins, err = manager.List(context.Background())
	if err != nil || len(plugins) != 0 {
		t.Fatalf("list after uninstall: %+v, %v", plugins, err)
	}
}

func TestManifestDigestValidation(t *testing.T) {
	t.Parallel()
	valid := "sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: valid, want: true},
		{value: "sha256:"},
		{value: valid + "00"},
		{value: "sha256:2D711642B726B04401627CA9FBAC32F5C8530FB1903CC4DB02258717921A4881"},
		{value: "md5:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"},
	} {
		if got := validManifestDigest(test.value); got != test.want {
			t.Errorf("validManifestDigest(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestListRejectsActiveDigestMismatch(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, root, 2)
	receipt, err := manager.Install(context.Background(), buildTestPackage(t, "1.0.0", []byte("one")))
	if err != nil {
		t.Fatal(err)
	}
	active := ActiveVersion{
		Version:        receipt.Version,
		ManifestDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		UpdatedAt:      time.Now().UTC(),
	}
	data, err := json.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, pluginsDirectory, receipt.ID, activeFilename)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.List(context.Background()); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("active digest mismatch error = %v", err)
	}
}

func TestInstallVersionCollisionAndRetention(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, root, 2)
	one := buildTestPackage(t, "1.0.0", []byte("one"))
	two := buildTestPackage(t, "2.0.0", []byte("two"))
	three := buildTestPackage(t, "3.0.0", []byte("three"))
	for _, filename := range []string{one, two, three} {
		if _, err := manager.Install(context.Background(), filename); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	plugins, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || len(plugins[0].Versions) != 2 || plugins[0].ActiveVersion != "3.0.0" {
		t.Fatalf("retention did not retain active plus newest predecessor: %+v", plugins)
	}
	if _, err := os.Stat(filepath.Join(root, pluginsDirectory, "example.hello", versionsDirectory, "1.0.0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old version still exists: %v", err)
	}

	collision := buildTestPackage(t, "3.0.0", []byte("different"))
	if _, err := manager.Install(context.Background(), collision); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestInstallLockSerializesOperationsAndLeavesNoLock(t *testing.T) {
	root := t.TempDir()
	manager := newTestManager(t, root, 2)
	release, err := manager.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := manager.List(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended list error = %v", err)
	}
	release()
	if _, err := manager.List(context.Background()); err != nil {
		t.Fatalf("list after release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, lockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remains after release: %v", err)
	}
}

func TestInstallSanitizesModesAndLeavesNoStagingDirectory(t *testing.T) {
	archive := buildTestPackage(t, "1.0.0", []byte("mode"))
	root := t.TempDir()
	manager := newTestManager(t, root, 2)
	receipt, err := manager.Install(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, pluginsDirectory, receipt.ID, versionsDirectory, receipt.Version, "bin", "hello")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(filepath.Dir(installed)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".staging-") && entry.Name()[:len(".staging-")] == ".staging-" {
			t.Fatalf("staging directory remains: %s", entry.Name())
		}
	}
}

func newTestManager(t *testing.T, root string, retain int) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerConfig{
		Root: root, RetainVersions: retain, LockWait: time.Second, StaleLockAge: time.Minute,
		Verify: VerifyOptions{
			Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustAllowUnsigned},
			OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: DefaultCoreVersion, ProtocolVersion: "1.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func buildTestPackage(t *testing.T, version string, payload []byte) string {
	t.Helper()
	source, manifest := createSource(t, version, payload, runtime.GOOS, runtime.GOARCH)
	filename := filepath.Join(t.TempDir(), version+".srnx")
	if _, err := Pack(context.Background(), source, manifest, filename, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	return filename
}
