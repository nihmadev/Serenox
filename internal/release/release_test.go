package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/manifest"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

const testManifest = `apiVersion: serenox.app/v1
kind: Application
metadata: {name: release-test, version: %VERSION%}
runtime: {dataDir: data, configMode: recorded, logLevel: info, shutdownTimeout: 20s}
telegram: {enabled: false, sessionFile: telegram.session.json}
storage: {backend: file, path: state.json}
services: {}
plugins: %PLUGINS%
release: {retainVersions: 2, healthTimeout: 5s}
`

func loadEffective(t *testing.T, dir string, version, plugins string) manifest.Effective {
	t.Helper()
	source := strings.NewReplacer("%VERSION%", version, "%PLUGINS%", plugins).Replace(testManifest)
	path := filepath.Join(dir, "serenox.yaml")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	effective, err := manifest.LoadEffective(manifest.LoadOptions{Path: path, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	return effective
}

func testManager() *Manager {
	manager := NewManager()
	current := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time {
		current = current.Add(time.Second)
		return current
	}
	return manager
}

func TestApplyRequiresApproval(t *testing.T) {
	effective := loadEffective(t, t.TempDir(), "1", "[]")
	if _, err := testManager().Apply(context.Background(), effective, false); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Apply without approval error = %v", err)
	}
	if _, err := os.Stat(releasesDir(effective.Runtime.DataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved apply wrote state: %v", err)
	}
}

func TestPlanApplyIsIdempotentAndStatusReportsActive(t *testing.T) {
	effective := loadEffective(t, t.TempDir(), "1", "[]")
	manager := testManager()
	plan, err := manager.Plan(context.Background(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || !plan.Changed || plan.ManifestDigest != effective.Digest {
		t.Fatalf("plan = %+v", plan)
	}
	first, err := manager.Apply(context.Background(), effective, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Apply(context.Background(), effective, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.State != StateHealthy {
		t.Fatalf("re-apply created %s after %s", second.ID, first.ID)
	}
	records, active, err := manager.Status(effective.Runtime.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || active == nil || active.ReleaseID != first.ID {
		t.Fatalf("status records=%d active=%+v", len(records), active)
	}
	staged, err := StagedManifestPath(effective.Runtime.DataDir, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(staged); err != nil || string(data) != string(effective.Source) {
		t.Fatalf("staged manifest = %q, %v", data, err)
	}
	plan, err = manager.Plan(context.Background(), effective)
	if err != nil || plan.Changed {
		t.Fatalf("plan after apply changed=%v err=%v", plan.Changed, err)
	}
}

func TestFailedPluginVerificationKeepsActivePointer(t *testing.T) {
	dir := t.TempDir()
	manager := testManager()
	healthy, err := manager.Apply(context.Background(), loadEffective(t, dir, "1", "[]"), true)
	if err != nil {
		t.Fatal(err)
	}
	manager.verify = func(context.Context, string, pluginpackage.VerifyOptions) (pluginpackage.VerifiedPackage, error) {
		return pluginpackage.VerifiedPackage{}, errors.New("signature mismatch")
	}
	broken := loadEffective(t, dir, "2", "[{id: example.echo, path: echo.srnx}]")
	record, err := manager.Apply(context.Background(), broken, true)
	if !errors.Is(err, ErrNotReady) || record.State != StateFailed {
		t.Fatalf("Apply broken = %+v, %v", record, err)
	}
	_, active, err := manager.Status(broken.Runtime.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.ReleaseID != healthy.ID {
		t.Fatalf("active moved to %s after failed apply", active.ReleaseID)
	}
}

func TestPluginIdentityMismatchIsNotReady(t *testing.T) {
	manager := testManager()
	manager.verify = func(_ context.Context, path string, _ pluginpackage.VerifyOptions) (pluginpackage.VerifiedPackage, error) {
		if filepath.Base(path) != "echo.srnx" {
			t.Fatalf("verified %s", path)
		}
		return pluginpackage.VerifiedPackage{Manifest: pluginpackage.Manifest{ID: "other.plugin", Version: "1.0.0"}, ManifestDigest: "abc"}, nil
	}
	effective := loadEffective(t, t.TempDir(), "1", "[{id: example.echo, path: echo.srnx}]")
	plan, err := manager.Plan(context.Background(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.Plugins) != 1 || !strings.Contains(plan.Plugins[0].Error, "other.plugin") {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRollbackAndRetention(t *testing.T) {
	dir := t.TempDir()
	manager := testManager()
	var applied []Record
	for _, version := range []string{"1", "2", "3"} {
		record, err := manager.Apply(context.Background(), loadEffective(t, dir, version, "[]"), true)
		if err != nil {
			t.Fatal(err)
		}
		applied = append(applied, record)
	}
	dataDir := filepath.Join(dir, "data")
	records, _, err := manager.Status(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != applied[2].ID || records[1].ID != applied[1].ID {
		t.Fatalf("retained %d records, want newest two", len(records))
	}
	if _, err := os.Stat(filepath.Join(releasesDir(dataDir), manifestsDir, applied[0].ID+".yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned staged manifest still exists: %v", err)
	}
	target, err := manager.Rollback(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != applied[1].ID {
		t.Fatalf("rollback selected %s, want %s", target.ID, applied[1].ID)
	}
	_, active, err := manager.Status(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.ReleaseID != applied[1].ID || active.RolledBackFrom != applied[2].ID {
		t.Fatalf("active after rollback = %+v", active)
	}
	if _, err := manager.Rollback(dataDir, ""); !errors.Is(err, ErrNoRollbackTarget) {
		t.Fatalf("second rollback error = %v", err)
	}
	if _, err := manager.Rollback(dataDir, "../escape"); err == nil {
		t.Fatal("rollback accepted an invalid release ID")
	}
}

func TestStatusWithoutReleases(t *testing.T) {
	records, active, err := NewManager().Status(t.TempDir())
	if err != nil || len(records) != 0 || active != nil {
		t.Fatalf("Status = %v, %+v, %v", records, active, err)
	}
}
