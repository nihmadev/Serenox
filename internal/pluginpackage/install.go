package pluginpackage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	pluginsDirectory  = "plugins"
	versionsDirectory = "versions"
	activeFilename    = "active.json"
	receiptFilename   = "receipt.json"
	lockFilename      = ".plugin-install.lock"
)

type ManagerConfig struct {
	Root           string
	Verify         VerifyOptions
	RetainVersions int
	LockWait       time.Duration
	StaleLockAge   time.Duration
}

type Manager struct {
	root           string
	verify         VerifyOptions
	retainVersions int
	lockWait       time.Duration
	staleLockAge   time.Duration
	now            func() time.Time
}

// RetainVersions reports the fixed installation retention bound. Runtime
// activation controllers require at least two so a failed upgrade can restore
// the previously active bytes; the standalone CLI may intentionally use one.
func (m *Manager) RetainVersions() int {
	if m == nil {
		return 0
	}
	return m.retainVersions
}

type Receipt struct {
	Format         string     `json:"format"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Version        string     `json:"version"`
	Category       Category   `json:"category"`
	ManifestDigest string     `json:"manifest_digest"`
	Entrypoint     Entrypoint `json:"entrypoint"`
	Capabilities   []string   `json:"capabilities,omitempty"`
	Permissions    []string   `json:"permissions,omitempty"`
	Signed         bool       `json:"signed"`
	SignerKeyID    string     `json:"signer_key_id,omitempty"`
	Trusted        bool       `json:"trusted"`
	InstalledAt    time.Time  `json:"installed_at"`
}

type ActiveVersion struct {
	Version        string    `json:"version"`
	ManifestDigest string    `json:"manifest_digest"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type InstalledVersion struct {
	Receipt Receipt `json:"receipt"`
	Path    string  `json:"path"`
	Active  bool    `json:"active"`
}

type InstalledPlugin struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Category      Category           `json:"category"`
	ActiveVersion string             `json:"active_version,omitempty"`
	Versions      []InstalledVersion `json:"versions"`
}

func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Root == "" {
		return nil, fmt.Errorf("plugin installation root is required")
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin installation root: %w", err)
	}
	limits, err := config.Verify.Limits.normalized()
	if err != nil {
		return nil, err
	}
	config.Verify.Limits = limits
	if config.Verify.Trust.Mode == "" {
		config.Verify.Trust.Mode = TrustAllowUnsigned
	}
	if err := validateTrustMode(config.Verify.Trust.Mode); err != nil {
		return nil, err
	}
	if config.Verify.OS == "" {
		config.Verify.OS = runtime.GOOS
	}
	if config.Verify.Arch == "" {
		config.Verify.Arch = runtime.GOARCH
	}
	if config.Verify.CoreVersion == "" {
		config.Verify.CoreVersion = DefaultCoreVersion
	}
	if config.Verify.ProtocolVersion == "" {
		config.Verify.ProtocolVersion = "1.0"
	}
	config.Verify.CheckPlatform = true
	config.Verify.CheckVersions = true
	if config.RetainVersions == 0 {
		config.RetainVersions = DefaultRetain
	}
	if config.RetainVersions < 1 || config.RetainVersions > 32 {
		return nil, fmt.Errorf("retain versions must be between 1 and 32")
	}
	if config.LockWait == 0 {
		config.LockWait = defaultLockWait * time.Second
	}
	if config.StaleLockAge == 0 {
		config.StaleLockAge = defaultStaleLockAge * time.Second
	}
	if config.LockWait < 0 || config.StaleLockAge < time.Second {
		return nil, fmt.Errorf("lock wait must be non-negative and stale lock age at least one second")
	}
	return &Manager{
		root: root, verify: config.Verify, retainVersions: config.RetainVersions,
		lockWait: config.LockWait, staleLockAge: config.StaleLockAge, now: time.Now,
	}, nil
}

func (m *Manager) Install(ctx context.Context, packageFilename string) (Receipt, error) {
	release, err := m.acquire(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	verified, archive, err := openAndVerify(ctx, packageFilename, m.verify)
	if err != nil {
		return Receipt{}, err
	}
	defer archive.close()
	manifest := verified.Manifest
	pluginDirectory := m.pluginDirectory(manifest.ID)
	versions := filepath.Join(pluginDirectory, versionsDirectory)
	if err := os.MkdirAll(versions, 0o700); err != nil {
		return Receipt{}, fmt.Errorf("create plugin versions directory: %w", err)
	}
	target := filepath.Join(versions, manifest.Version)
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Receipt{}, fmt.Errorf("%w: existing version path is not a regular directory", ErrInvalidPackage)
		}
		existing, readErr := readReceipt(target)
		if readErr != nil {
			return Receipt{}, readErr
		}
		if existing.ManifestDigest != verified.ManifestDigest {
			return Receipt{}, fmt.Errorf("%w: %s@%s", ErrAlreadyInstalled, manifest.ID, manifest.Version)
		}
		if err := m.writeActive(pluginDirectory, existing); err != nil {
			return Receipt{}, err
		}
		return existing, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Receipt{}, statErr
	}
	staging, err := os.MkdirTemp(versions, ".staging-")
	if err != nil {
		return Receipt{}, fmt.Errorf("create install staging directory: %w", err)
	}
	stagingExists := true
	defer func() {
		if stagingExists {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o700); err != nil {
		return Receipt{}, err
	}
	if err := extractVerified(ctx, archive, verified, staging, m.verify.Limits); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{
		Format: FormatV1, ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Category: manifest.Category, ManifestDigest: verified.ManifestDigest,
		Entrypoint:   verified.Entrypoint,
		Capabilities: append([]string(nil), manifest.Capabilities...),
		Permissions:  append([]string(nil), manifest.Permissions...),
		Signed:       verified.Signed, SignerKeyID: verified.SignerKeyID,
		Trusted: verified.Trusted, InstalledAt: m.now().UTC(),
	}
	if err := writeReceipt(staging, receipt); err != nil {
		return Receipt{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return Receipt{}, fmt.Errorf("sync staged plugin: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return Receipt{}, fmt.Errorf("commit plugin version: %w", err)
	}
	stagingExists = false
	if err := syncDirectory(versions); err != nil {
		return Receipt{}, fmt.Errorf("sync plugin versions: %w", err)
	}
	if err := m.writeActive(pluginDirectory, receipt); err != nil {
		return Receipt{}, err
	}
	if err := m.pruneVersions(pluginDirectory, receipt.Version); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (m *Manager) List(ctx context.Context) ([]InstalledPlugin, error) {
	release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.listUnlocked()
}

func (m *Manager) Rollback(ctx context.Context, pluginID, targetVersion string) (Receipt, error) {
	if !idPattern.MatchString(pluginID) {
		return Receipt{}, invalid("id", "invalid plugin id")
	}
	if targetVersion != "" && (!versionPattern.MatchString(targetVersion) || targetVersion == "." || targetVersion == "..") {
		return Receipt{}, invalid("version", "invalid target version")
	}
	release, err := m.acquire(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	pluginDirectory := m.pluginDirectory(pluginID)
	active, err := readActive(pluginDirectory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("%w: %s", ErrNotInstalled, pluginID)
		}
		return Receipt{}, err
	}
	versions, err := listVersions(pluginDirectory)
	if err != nil {
		return Receipt{}, err
	}
	var selected *Receipt
	for index := range versions {
		receipt := versions[index]
		if targetVersion != "" {
			if receipt.Version == targetVersion {
				selected = &receipt
				break
			}
		} else if receipt.Version != active.Version {
			selected = &receipt
			break
		}
	}
	if selected == nil || (targetVersion == "" && selected.Version == active.Version) {
		return Receipt{}, fmt.Errorf("%w: %s", ErrNoRollback, pluginID)
	}
	if selected.Version == active.Version {
		return *selected, nil
	}
	if err := m.writeActive(pluginDirectory, *selected); err != nil {
		return Receipt{}, err
	}
	return *selected, nil
}

func (m *Manager) Uninstall(ctx context.Context, pluginID, version string) error {
	if !idPattern.MatchString(pluginID) {
		return invalid("id", "invalid plugin id")
	}
	if version != "" && (!versionPattern.MatchString(version) || version == "." || version == "..") {
		return invalid("version", "invalid version")
	}
	release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	pluginDirectory := m.pluginDirectory(pluginID)
	info, err := os.Lstat(pluginDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotInstalled, pluginID)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: invalid installed plugin directory", ErrInvalidPackage)
	}
	if version == "" {
		return os.RemoveAll(pluginDirectory)
	}
	versionDirectory := filepath.Join(pluginDirectory, versionsDirectory, version)
	if _, err := os.Lstat(versionDirectory); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s@%s", ErrNotInstalled, pluginID, version)
	} else if err != nil {
		return err
	}
	active, activeErr := readActive(pluginDirectory)
	if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
		return activeErr
	}
	if err := os.RemoveAll(versionDirectory); err != nil {
		return err
	}
	if activeErr == nil && active.Version == version {
		versions, err := listVersions(pluginDirectory)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			if err := os.Remove(filepath.Join(pluginDirectory, activeFilename)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err := m.writeActive(pluginDirectory, versions[0]); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) listUnlocked() ([]InstalledPlugin, error) {
	pluginsRoot := filepath.Join(m.root, pluginsDirectory)
	entries, err := os.ReadDir(pluginsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []InstalledPlugin{}, nil
	}
	if err != nil {
		return nil, err
	}
	plugins := make([]InstalledPlugin, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		pluginDirectory := filepath.Join(pluginsRoot, entry.Name())
		versions, err := listVersions(pluginDirectory)
		if err != nil {
			return nil, err
		}
		if len(versions) == 0 {
			continue
		}
		active, activeErr := readActive(pluginDirectory)
		if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
			return nil, activeErr
		}
		plugin := InstalledPlugin{ID: entry.Name(), Name: versions[0].Name, Category: versions[0].Category, Versions: make([]InstalledVersion, 0, len(versions))}
		if activeErr == nil {
			matched := false
			for _, receipt := range versions {
				if receipt.Version != active.Version {
					continue
				}
				matched = true
				if receipt.ManifestDigest != active.ManifestDigest {
					return nil, fmt.Errorf("%w: active digest does not match receipt for %s@%s", ErrInvalidPackage, entry.Name(), active.Version)
				}
				break
			}
			if !matched {
				return nil, fmt.Errorf("%w: active version is missing for %s@%s", ErrInvalidPackage, entry.Name(), active.Version)
			}
			plugin.ActiveVersion = active.Version
		}
		for _, receipt := range versions {
			plugin.Versions = append(plugin.Versions, InstalledVersion{
				Receipt: receipt,
				Path:    filepath.Join(pluginDirectory, versionsDirectory, receipt.Version),
				Active:  receipt.Version == plugin.ActiveVersion,
			})
		}
		plugins = append(plugins, plugin)
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].ID < plugins[j].ID })
	return plugins, nil
}

func extractVerified(ctx context.Context, archive *archiveHandle, verified VerifiedPackage, destination string, limits Limits) error {
	for _, metadata := range verified.Manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := archive.entries[metadata.Path]
		target := filepath.Join(destination, filepath.FromSlash(metadata.Path))
		if !pathWithin(destination, target) {
			return fmt.Errorf("%w: %q", ErrUnsafeArchivePath, metadata.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(metadata.Mode))
		if err != nil {
			_ = reader.Close()
			return err
		}
		hasher := sha256.New()
		size, copyErr := copyWithContext(ctx, file, reader, limits.MaxFileBytes+1, hasher)
		readerErr := reader.Close()
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if readerErr != nil {
			return readerErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual := hex.EncodeToString(hasher.Sum(nil))
		if size != metadata.Size || actual != metadata.SHA256 {
			return fmt.Errorf("%w: file %q changed between verification and extraction", ErrIntegrity, metadata.Path)
		}
		// Ignore ZIP mode bits and apply only the normalized manifest mode.
		if err := os.Chmod(target, fs.FileMode(metadata.Mode)); err != nil {
			return err
		}
	}
	metadataDirectory := filepath.Join(destination, ReceiptDirectory)
	if err := os.MkdirAll(metadataDirectory, 0o700); err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(metadataDirectory, ManifestName), archive.manifestBytes, 0o600); err != nil {
		return err
	}
	if archive.signature != nil {
		if err := writeNewFile(filepath.Join(metadataDirectory, SignatureName), archive.signature, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) pruneVersions(pluginDirectory, activeVersion string) error {
	versions, err := listVersions(pluginDirectory)
	if err != nil {
		return err
	}
	keep := map[string]struct{}{activeVersion: {}}
	for _, receipt := range versions {
		if receipt.Version == activeVersion {
			continue
		}
		if len(keep) < m.retainVersions {
			keep[receipt.Version] = struct{}{}
		}
	}
	for _, receipt := range versions {
		if _, retained := keep[receipt.Version]; retained {
			continue
		}
		if err := os.RemoveAll(filepath.Join(pluginDirectory, versionsDirectory, receipt.Version)); err != nil {
			return err
		}
	}
	return nil
}

func listVersions(pluginDirectory string) ([]Receipt, error) {
	directory := filepath.Join(pluginDirectory, versionsDirectory)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Receipt{}, nil
	}
	if err != nil {
		return nil, err
	}
	versions := make([]Receipt, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".staging-") || !versionPattern.MatchString(entry.Name()) {
			continue
		}
		receipt, err := readReceipt(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if receipt.Version != entry.Name() {
			return nil, fmt.Errorf("%w: receipt version does not match directory", ErrInvalidPackage)
		}
		versions = append(versions, receipt)
	}
	sort.Slice(versions, func(i, j int) bool {
		if !versions[i].InstalledAt.Equal(versions[j].InstalledAt) {
			return versions[i].InstalledAt.After(versions[j].InstalledAt)
		}
		return versions[i].Version > versions[j].Version
	})
	return versions, nil
}

func writeReceipt(versionDirectory string, receipt Receipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeNewFile(filepath.Join(versionDirectory, ReceiptDirectory, receiptFilename), data, 0o600)
}

func readReceipt(versionDirectory string) (Receipt, error) {
	data, err := os.ReadFile(filepath.Join(versionDirectory, ReceiptDirectory, receiptFilename))
	if err != nil {
		return Receipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt Receipt
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Receipt{}, err
	}
	if receipt.Format != FormatV1 || !idPattern.MatchString(receipt.ID) || strings.TrimSpace(receipt.Name) == "" || len(receipt.Name) > 128 ||
		!versionPattern.MatchString(receipt.Version) || !validManifestDigest(receipt.ManifestDigest) ||
		(receipt.Category != CategoryGoExecutable && receipt.Category != CategoryPythonSidecar) {
		return Receipt{}, fmt.Errorf("%w: invalid installation receipt", ErrInvalidPackage)
	}
	if receipt.Trusted && !receipt.Signed || receipt.Signed && receipt.SignerKeyID == "" || !receipt.Signed && receipt.SignerKeyID != "" {
		return Receipt{}, fmt.Errorf("%w: inconsistent receipt signature state", ErrInvalidPackage)
	}
	if (!platformPattern.MatchString(receipt.Entrypoint.OS) && receipt.Entrypoint.OS != "any") ||
		(!platformPattern.MatchString(receipt.Entrypoint.Arch) && receipt.Entrypoint.Arch != "any") {
		return Receipt{}, fmt.Errorf("%w: invalid receipt entrypoint platform", ErrInvalidPackage)
	}
	cleaned, pathErr := cleanArchivePath(receipt.Entrypoint.Path)
	if pathErr != nil || cleaned != receipt.Entrypoint.Path {
		return Receipt{}, fmt.Errorf("%w: invalid receipt entrypoint path", ErrInvalidPackage)
	}
	if receipt.Category == CategoryGoExecutable && (receipt.Entrypoint.OS == "any" || receipt.Entrypoint.Arch == "any") {
		return Receipt{}, fmt.Errorf("%w: non-specific Go receipt entrypoint", ErrInvalidPackage)
	}
	if err := validateReceiptTokens("capabilities", receipt.Capabilities); err != nil {
		return Receipt{}, err
	}
	if err := validateReceiptTokens("permissions", receipt.Permissions); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateReceiptTokens(field string, values []string) error {
	if len(values) > 64 || !sort.StringsAreSorted(values) {
		return fmt.Errorf("%w: invalid receipt %s", ErrInvalidPackage, field)
	}
	for index, value := range values {
		if !tokenPattern.MatchString(value) || index > 0 && values[index-1] == value {
			return fmt.Errorf("%w: invalid receipt %s", ErrInvalidPackage, field)
		}
	}
	return nil
}

func (m *Manager) writeActive(pluginDirectory string, receipt Receipt) error {
	active := ActiveVersion{Version: receipt.Version, ManifestDigest: receipt.ManifestDigest, UpdatedAt: m.now().UTC()}
	data, err := json.Marshal(active)
	if err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(pluginDirectory, activeFilename), data, 0o600)
}

func readActive(pluginDirectory string) (ActiveVersion, error) {
	data, err := os.ReadFile(filepath.Join(pluginDirectory, activeFilename))
	if err != nil {
		return ActiveVersion{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var active ActiveVersion
	if err := decoder.Decode(&active); err != nil {
		return ActiveVersion{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ActiveVersion{}, err
	}
	if !versionPattern.MatchString(active.Version) || !validManifestDigest(active.ManifestDigest) {
		return ActiveVersion{}, fmt.Errorf("%w: invalid active version pointer", ErrInvalidPackage)
	}
	return active, nil
}

func validManifestDigest(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && hexDigestPattern.MatchString(strings.TrimPrefix(value, prefix))
}

func writeNewFile(filename string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeAtomicFile(filename string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".atomic-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryName, filename); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(filename))
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (m *Manager) pluginDirectory(id string) string {
	return filepath.Join(m.root, pluginsDirectory, id)
}

type installLock struct {
	PID       int       `json:"pid"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

func (m *Manager) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(m.root, lockFilename)
	deadline := time.NewTimer(m.lockWait)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("generate installation lock token: %w", err)
		}
		lock := installLock{PID: os.Getpid(), Token: hex.EncodeToString(tokenBytes), CreatedAt: m.now().UTC()}
		data, _ := json.Marshal(lock)
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.Write(data); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, closeErr
			}
			return func() {
				current, readErr := os.ReadFile(lockPath)
				if readErr == nil && bytes.Equal(current, data) {
					_ = os.Remove(lockPath)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && m.now().Sub(info.ModTime()) > m.staleLockAge {
			staleName := fmt.Sprintf("%s.stale-%d", lockPath, m.now().UnixNano())
			if renameErr := os.Rename(lockPath, staleName); renameErr == nil {
				_ = os.Remove(staleName)
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrInstallLockTimeout
		case <-ticker.C:
		}
	}
}
