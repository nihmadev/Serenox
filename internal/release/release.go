// Package release records declarative manifest releases beneath the runtime
// data directory. A release is a verified, redacted snapshot of one canonical
// manifest digest; the active pointer only moves after every check passes.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/manifest"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

const (
	RecordFormat = "serenox.release/v1"
	ActiveFormat = "serenox.release-active/v1"

	activeFilename    = "active.json"
	lockFilename      = ".release.lock"
	manifestsDir      = "manifests"
	maxRecordBytes    = 1 << 20
	maxRecords        = 256
	maxProblemBytes   = 512
	defaultRetain     = 3
	defaultHealthWait = 30 * time.Second
)

type State string

const (
	StateHealthy State = "healthy"
	StateFailed  State = "failed"
)

var (
	ErrApprovalRequired = errors.New("release: apply requires --yes")
	ErrNotReady         = errors.New("release: plan is not ready")
	ErrNoRollbackTarget = errors.New("release: no retained healthy release to roll back to")
	ErrLocked           = errors.New("release: another release operation is in progress")

	releaseIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}$`)
)

// PluginCheck is the redacted verification result of one manifest plugin
// reference. Package paths are recorded as the manifest's local filename only.
type PluginCheck struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Version        string `json:"version,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	Signed         bool   `json:"signed"`
	Trusted        bool   `json:"trusted"`
	Error          string `json:"error,omitempty"`
}

type Plan struct {
	Name           string                   `json:"name"`
	Version        int                      `json:"version"`
	ManifestDigest string                   `json:"manifest_digest"`
	ActiveRelease  string                   `json:"active_release,omitempty"`
	ActiveDigest   string                   `json:"active_digest,omitempty"`
	Changed        bool                     `json:"changed"`
	Ready          bool                     `json:"ready"`
	Services       []manifest.ServiceStatus `json:"services"`
	Plugins        []PluginCheck            `json:"plugins"`
	Problems       []string                 `json:"problems,omitempty"`
	PlannedAt      time.Time                `json:"planned_at"`
}

type Record struct {
	Format         string                   `json:"format"`
	ID             string                   `json:"id"`
	Name           string                   `json:"name"`
	Version        int                      `json:"version"`
	ManifestDigest string                   `json:"manifest_digest"`
	State          State                    `json:"state"`
	CreatedAt      time.Time                `json:"created_at"`
	Services       []manifest.ServiceStatus `json:"services"`
	Plugins        []PluginCheck            `json:"plugins"`
	Problems       []string                 `json:"problems,omitempty"`
}

type Active struct {
	Format         string    `json:"format"`
	ReleaseID      string    `json:"release_id"`
	ManifestDigest string    `json:"manifest_digest"`
	ActivatedAt    time.Time `json:"activated_at"`
	RolledBackFrom string    `json:"rolled_back_from,omitempty"`
}

// PluginVerifier verifies one local .srnx package. It is replaceable so tests
// can avoid building signed archives.
type PluginVerifier func(ctx context.Context, path string, options pluginpackage.VerifyOptions) (pluginpackage.VerifiedPackage, error)

type Manager struct {
	now      func() time.Time
	verify   PluginVerifier
	registry func() *manifest.Registry
}

func NewManager() *Manager {
	return &Manager{
		now:    func() time.Time { return time.Now().UTC() },
		verify: pluginpackage.Verify,
		registry: func() *manifest.Registry {
			registry := manifest.NewRegistry()
			registry.SetProbe("postgres", manifest.TCPProbe{})
			registry.SetProbe("redis", manifest.TCPProbe{})
			registry.SetProbe("object_storage", manifest.TCPProbe{})
			return registry
		},
	}
}

// Plan checks the effective manifest without writing anything.
func (m *Manager) Plan(ctx context.Context, effective manifest.Effective) (Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if effective.Digest == "" {
		return Plan{}, errors.New("release: effective manifest has no digest")
	}
	dir := releasesDir(effective.Runtime.DataDir)
	plan := Plan{
		Name: effective.Document.Metadata.Name, Version: effective.Document.Metadata.Version,
		ManifestDigest: effective.Digest, PlannedAt: m.now(),
		Services: []manifest.ServiceStatus{}, Plugins: []PluginCheck{},
	}
	active, err := readActive(dir)
	if err != nil {
		return Plan{}, err
	}
	if active != nil {
		plan.ActiveRelease, plan.ActiveDigest = active.ReleaseID, active.ManifestDigest
	}
	plan.Changed = active == nil || active.ManifestDigest != effective.Digest

	if err := effective.BackendSupported(); err != nil {
		plan.Problems = append(plan.Problems, problem(err))
	}
	statuses, err := m.registry().CheckDeclared(ctx, effective.Document)
	plan.Services = append(plan.Services, statuses...)
	if err != nil {
		plan.Problems = append(plan.Problems, problem(err))
	}
	options, err := verifyOptions(effective)
	if err != nil {
		plan.Problems = append(plan.Problems, problem(err))
	} else {
		baseDir := filepath.Dir(effective.ManifestPath)
		for _, ref := range effective.Document.Plugins {
			check := m.checkPlugin(ctx, baseDir, ref, options)
			if check.Error != "" {
				plan.Problems = append(plan.Problems, fmt.Sprintf("plugin %s: %s", ref.ID, check.Error))
			}
			plan.Plugins = append(plan.Plugins, check)
		}
	}
	plan.Ready = len(plan.Problems) == 0
	return plan, nil
}

// Apply records and activates a ready plan. Re-applying the active healthy
// digest is a no-op that returns the active record. A failed plan is recorded
// for diagnosis but never moves the active pointer.
func (m *Manager) Apply(ctx context.Context, effective manifest.Effective, approved bool) (Record, error) {
	if !approved {
		return Record{}, ErrApprovalRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dir := releasesDir(effective.Runtime.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Record{}, fmt.Errorf("release: create releases directory: %w", err)
	}
	unlock, err := lock(dir)
	if err != nil {
		return Record{}, err
	}
	defer unlock()

	if active, err := readActive(dir); err != nil {
		return Record{}, err
	} else if active != nil && active.ManifestDigest == effective.Digest {
		record, err := readRecord(dir, active.ReleaseID)
		if err == nil && record.State == StateHealthy {
			return record, nil
		}
	}

	timeout := effective.Document.Release.HealthTimeout.Duration
	if timeout <= 0 {
		timeout = defaultHealthWait
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	plan, err := m.Plan(checkCtx, effective)
	cancel()
	if err != nil {
		return Record{}, err
	}
	createdAt := m.now()
	record := Record{
		Format: RecordFormat, ID: createdAt.Format("20060102T150405Z") + "-" + effective.Digest[:12],
		Name: plan.Name, Version: plan.Version, ManifestDigest: plan.ManifestDigest,
		State: StateHealthy, CreatedAt: createdAt,
		Services: plan.Services, Plugins: plan.Plugins, Problems: plan.Problems,
	}
	if !plan.Ready {
		record.State = StateFailed
		if err := writeRecord(dir, record); err != nil {
			return Record{}, err
		}
		return record, fmt.Errorf("%w: %s", ErrNotReady, strings.Join(plan.Problems, "; "))
	}
	if err := stageManifest(dir, record.ID, effective.Source); err != nil {
		return Record{}, err
	}
	if err := writeRecord(dir, record); err != nil {
		return Record{}, err
	}
	if err := writeActive(dir, Active{Format: ActiveFormat, ReleaseID: record.ID, ManifestDigest: record.ManifestDigest, ActivatedAt: createdAt}); err != nil {
		return Record{}, err
	}
	retain := effective.Document.Release.RetainVersions
	if retain <= 0 {
		retain = defaultRetain
	}
	if err := prune(dir, retain, record.ID); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Status lists retained records, newest first, and the active pointer.
func (m *Manager) Status(dataDir string) ([]Record, *Active, error) {
	dir := releasesDir(dataDir)
	records, err := listRecords(dir)
	if err != nil {
		return nil, nil, err
	}
	active, err := readActive(dir)
	if err != nil {
		return nil, nil, err
	}
	return records, active, nil
}

// Rollback points the active release at a retained healthy record. With an
// empty ID it selects the newest healthy release older than the active one.
// External side effects of the newer release are not undone.
func (m *Manager) Rollback(dataDir, releaseID string) (Record, error) {
	dir := releasesDir(dataDir)
	unlock, err := lock(dir)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	active, err := readActive(dir)
	if err != nil {
		return Record{}, err
	}
	var target Record
	if releaseID != "" {
		target, err = readRecord(dir, releaseID)
		if err != nil {
			return Record{}, err
		}
		if target.State != StateHealthy {
			return Record{}, fmt.Errorf("release: %s is %s and cannot be activated", releaseID, target.State)
		}
	} else {
		records, err := listRecords(dir)
		if err != nil {
			return Record{}, err
		}
		var activeRecord *Record
		if active != nil {
			for i := range records {
				if records[i].ID == active.ReleaseID {
					activeRecord = &records[i]
					break
				}
			}
		}
		found := false
		for _, record := range records {
			if record.State != StateHealthy || (active != nil && record.ID == active.ReleaseID) {
				continue
			}
			if activeRecord != nil && !record.CreatedAt.Before(activeRecord.CreatedAt) {
				continue
			}
			target, found = record, true
			break
		}
		if !found {
			return Record{}, ErrNoRollbackTarget
		}
	}
	if active != nil && active.ReleaseID == target.ID {
		return target, nil
	}
	if _, err := os.Stat(filepath.Join(dir, manifestsDir, target.ID+".yaml")); err != nil {
		return Record{}, fmt.Errorf("release: staged manifest for %s is missing: %w", target.ID, err)
	}
	next := Active{Format: ActiveFormat, ReleaseID: target.ID, ManifestDigest: target.ManifestDigest, ActivatedAt: m.now()}
	if active != nil {
		next.RolledBackFrom = active.ReleaseID
	}
	if err := writeActive(dir, next); err != nil {
		return Record{}, err
	}
	return target, nil
}

// StagedManifestPath returns the manifest snapshot recorded for a release.
func StagedManifestPath(dataDir, releaseID string) (string, error) {
	if !releaseIDPattern.MatchString(releaseID) {
		return "", fmt.Errorf("release: invalid release ID %q", releaseID)
	}
	return filepath.Join(releasesDir(dataDir), manifestsDir, releaseID+".yaml"), nil
}

func (m *Manager) checkPlugin(ctx context.Context, baseDir string, ref manifest.PluginRef, options pluginpackage.VerifyOptions) PluginCheck {
	check := PluginCheck{ID: ref.ID, Path: ref.Path, Version: ref.Version}
	if filepath.Base(ref.Path) != ref.Path || !strings.HasSuffix(ref.Path, ".srnx") {
		check.Error = "path must be a local .srnx filename"
		return check
	}
	verified, err := m.verify(ctx, filepath.Join(baseDir, ref.Path), options)
	if err != nil {
		check.Error = problem(err)
		return check
	}
	check.ManifestDigest, check.Signed, check.Trusted = verified.ManifestDigest, verified.Signed, verified.Trusted
	switch {
	case ref.ID != "" && verified.Manifest.ID != ref.ID:
		check.Error = fmt.Sprintf("package declares id %q", verified.Manifest.ID)
	case ref.Version != "" && verified.Manifest.Version != ref.Version:
		check.Error = fmt.Sprintf("package declares version %q", verified.Manifest.Version)
	}
	if check.Version == "" {
		check.Version = verified.Manifest.Version
	}
	return check
}

func verifyOptions(effective manifest.Effective) (pluginpackage.VerifyOptions, error) {
	options := pluginpackage.DefaultVerifyOptions()
	options.Trust = pluginpackage.TrustPolicy{Mode: pluginpackage.TrustMode(effective.Runtime.Plugins.TrustMode)}
	if options.Trust.Mode == "" {
		options.Trust.Mode = pluginpackage.TrustAllowUnsigned
	}
	if trustPath := effective.Runtime.PluginTrustPath(); trustPath != "" {
		loaded, err := pluginpackage.LoadTrustPolicy(trustPath)
		if err != nil {
			return pluginpackage.VerifyOptions{}, fmt.Errorf("load plugin trust policy: %w", err)
		}
		loaded.Mode = options.Trust.Mode
		options.Trust = loaded
	}
	return options, nil
}

func releasesDir(dataDir string) string {
	return filepath.Join(dataDir, ".serenox", "releases")
}

func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("release: create releases directory: %w", err)
	}
	path := filepath.Join(dir, lockFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, fmt.Errorf("release: acquire lock: %w", err)
	}
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Close()
	return func() { _ = os.Remove(path) }, nil
}

func listRecords(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("release: list records: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		id := strings.TrimSuffix(name, ".json")
		if entry.IsDir() || name == id || !releaseIDPattern.MatchString(id) {
			continue
		}
		if len(records) == maxRecords {
			return nil, fmt.Errorf("release: more than %d records in %s", maxRecords, dir)
		}
		record, err := readRecord(dir, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}

func readRecord(dir, id string) (Record, error) {
	if !releaseIDPattern.MatchString(id) {
		return Record{}, fmt.Errorf("release: invalid release ID %q", id)
	}
	var record Record
	if err := readJSON(filepath.Join(dir, id+".json"), &record); err != nil {
		return Record{}, err
	}
	if record.Format != RecordFormat || record.ID != id {
		return Record{}, fmt.Errorf("release: record %s is malformed", id)
	}
	return record, nil
}

func readActive(dir string) (*Active, error) {
	var active Active
	err := readJSON(filepath.Join(dir, activeFilename), &active)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if active.Format != ActiveFormat || !releaseIDPattern.MatchString(active.ReleaseID) {
		return nil, errors.New("release: active pointer is malformed")
	}
	return &active, nil
}

func readJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("release: open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return fmt.Errorf("release: read %s: %w", filepath.Base(path), err)
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("release: %s exceeds %d bytes", filepath.Base(path), maxRecordBytes)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("release: decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeRecord(dir string, record Record) error {
	return writeJSON(filepath.Join(dir, record.ID+".json"), record)
}

func writeActive(dir string, active Active) error {
	return writeJSON(filepath.Join(dir, activeFilename), active)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return fmt.Errorf("release: %s exceeds %d bytes", filepath.Base(path), maxRecordBytes)
	}
	return writeAtomic(path, data)
}

func stageManifest(dir, id string, source []byte) error {
	if len(source) == 0 {
		return errors.New("release: effective manifest source is empty")
	}
	if len(source) > manifest.MaxFileBytes {
		return errors.New("release: manifest source exceeds the manifest size limit")
	}
	staged := filepath.Join(dir, manifestsDir)
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return fmt.Errorf("release: create staged manifest directory: %w", err)
	}
	return writeAtomic(filepath.Join(staged, id+".yaml"), source)
}

// prune keeps the newest retain records plus the active one; failed records
// count toward the bound so repeated failures cannot grow the directory.
func prune(dir string, retain int, activeID string) error {
	records, err := listRecords(dir)
	if err != nil {
		return err
	}
	kept := 0
	for _, record := range records {
		if record.ID == activeID || kept < retain {
			kept++
			continue
		}
		if err := os.Remove(filepath.Join(dir, record.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("release: prune %s: %w", record.ID, err)
		}
		if err := os.Remove(filepath.Join(dir, manifestsDir, record.ID+".yaml")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("release: prune staged manifest %s: %w", record.ID, err)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) (retErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("release: create temporary file: %w", err)
	}
	name := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if retErr != nil {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("release: chmod temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("release: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("release: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("release: close temporary file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("release: replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

func problem(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > maxProblemBytes {
		text = text[:maxProblemBytes] + "…"
	}
	return text
}
