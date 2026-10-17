package manifest

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupFormat = "serenox.backup/v1"

type BackupEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupManifest struct {
	Format    string        `json:"format"`
	CreatedAt time.Time     `json:"created_at"`
	DataDir   string        `json:"data_dir"`
	Entries   []BackupEntry `json:"entries"`
	Secrets   string        `json:"secrets"`
	MaxBytes  int64         `json:"max_bytes"`
}

type BackupOptions struct {
	OutputPath string
	MaxBytes   int64
}

func CreateBackup(effective Effective, options BackupOptions) (string, error) {
	if effective.Document.Storage.Backend != "file" {
		return "", fmt.Errorf("backup: %w for backend %q", ErrUnsupportedBackend, effective.Document.Storage.Backend)
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = 64 << 20
	}
	if maxBytes < 1 || maxBytes > 1<<30 {
		return "", errors.New("backup: maxBytes must be between 1 and 1 GiB")
	}
	statePath := effective.Runtime.StoragePath()
	if _, err := os.Stat(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("backup: inspect state: %w", err)
	}
	entries := make([]BackupEntry, 0, 2)
	if info, err := os.Stat(statePath); err == nil {
		entry, err := entryFor(statePath, "storage/state.json", info.Size(), maxBytes)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry)
	}
	releaseDir := filepath.Join(effective.Runtime.DataDir, ".serenox", "releases")
	if info, err := os.Stat(releaseDir); err == nil && info.IsDir() {
		files, err := os.ReadDir(releaseDir)
		if err != nil {
			return "", err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			path := filepath.Join(releaseDir, file.Name())
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			entry, err := entryFor(path, filepath.ToSlash(filepath.Join("releases", file.Name())), info.Size(), maxBytes)
			if err != nil {
				return "", err
			}
			entries = append(entries, entry)
		}
	}
	manifest := BackupManifest{Format: backupFormat, CreatedAt: time.Now().UTC(), DataDir: filepath.Base(effective.Runtime.DataDir), Entries: entries, Secrets: "excluded", MaxBytes: maxBytes}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	output := options.OutputPath
	if output == "" {
		output = filepath.Join(effective.Runtime.DataDir, "backups", "serenox-"+manifest.CreatedAt.Format("20060102T150405Z")+".srb")
	}
	if err := writeArchive(output, effective.Runtime.DataDir, data, entries); err != nil {
		return "", err
	}
	return output, nil
}

func VerifyBackup(path string, maxBytes int64) (BackupManifest, error) {
	if maxBytes == 0 {
		maxBytes = 64 << 20
	}
	if maxBytes < 1 || maxBytes > 1<<30 {
		return BackupManifest{}, errors.New("backup: invalid verification limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return BackupManifest{}, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return BackupManifest{}, fmt.Errorf("backup: gzip: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, maxBytes+1))
	var manifest BackupManifest
	seen := make(map[string]bool)
	contents := make(map[string][]byte)
	var totalBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return BackupManifest{}, fmt.Errorf("backup: archive: %w", err)
		}
		if header.Size < 0 || header.Size > maxBytes || !safeArchiveName(header.Name) || seen[header.Name] {
			return BackupManifest{}, errors.New("backup: invalid or duplicate archive entry")
		}
		seen[header.Name] = true
		content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
		if err != nil || int64(len(content)) != header.Size {
			return BackupManifest{}, errors.New("backup: truncated entry")
		}
		totalBytes += int64(len(content))
		if totalBytes > maxBytes {
			return BackupManifest{}, errors.New("backup: total archive content exceeds limit")
		}
		contents[header.Name] = content
		if header.Name == "backup.json" {
			if err := json.Unmarshal(content, &manifest); err != nil {
				return BackupManifest{}, fmt.Errorf("backup: manifest: %w", err)
			}
		}
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, maxBytes+1)); err != nil {
		return BackupManifest{}, fmt.Errorf("backup: gzip checksum: %w", err)
	}
	if err := gz.Close(); err != nil {
		return BackupManifest{}, fmt.Errorf("backup: gzip close: %w", err)
	}
	if manifest.Format != backupFormat {
		return BackupManifest{}, errors.New("backup: unsupported format")
	}
	for _, entry := range manifest.Entries {
		if !safeArchiveName(entry.Name) || entry.Size < 0 || entry.Size > maxBytes || len(entry.SHA256) != 64 {
			return BackupManifest{}, errors.New("backup: invalid manifest entry")
		}
		content, exists := contents[entry.Name]
		if !exists || int64(len(content)) != entry.Size {
			return BackupManifest{}, errors.New("backup: manifest entry is missing or truncated")
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			return BackupManifest{}, fmt.Errorf("backup: checksum mismatch for %s", entry.Name)
		}
	}
	if !seen["backup.json"] {
		return BackupManifest{}, errors.New("backup: manifest entry is missing")
	}
	return manifest, nil
}

func RestoreBackup(effective Effective, input string) error {
	if effective.Document.Storage.Backend != "file" {
		return fmt.Errorf("backup restore: %w", ErrUnsupportedBackend)
	}
	if _, err := os.Stat(filepath.Join(effective.Runtime.DataDir, ".serenox", "control.sock")); err == nil {
		return errors.New("backup restore: daemon control socket exists; stop or fence Serenox first")
	}
	manifest, err := VerifyBackup(input, 64<<20)
	if err != nil {
		return err
	}
	var state []byte
	if manifest.Secrets != "excluded" {
		return errors.New("backup restore: secret policy is not supported")
	}
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if header.Name == "storage/state.json" {
			state, err = io.ReadAll(io.LimitReader(reader, 32<<20+1))
			if err != nil || len(state) > 32<<20 {
				return errors.New("backup restore: state exceeds file-store limit")
			}
			break
		}
	}
	if state == nil {
		return errors.New("backup restore: storage/state.json is missing")
	}
	if !json.Valid(state) {
		return errors.New("backup restore: state is not JSON")
	}
	return atomicReplace(effective.Runtime.StoragePath(), state)
}

func entryFor(path, name string, size, maxBytes int64) (BackupEntry, error) {
	if size < 0 || size > maxBytes {
		return BackupEntry{}, fmt.Errorf("backup: %s exceeds size limit", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return BackupEntry{}, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil || written != size {
		return BackupEntry{}, fmt.Errorf("backup: read %s", name)
	}
	return BackupEntry{Name: name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func writeArchive(output, dataDir string, manifest []byte, entries []BackupEntry) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".backup-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	gz := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gz)
	if err := writeTarBytes(tarWriter, "backup.json", manifest); err != nil {
		temporary.Close()
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dataDir, "state.json")
		if strings.HasPrefix(entry.Name, "releases/") {
			path = filepath.Join(dataDir, ".serenox", "releases", strings.TrimPrefix(entry.Name, "releases/"))
		}
		file, err := os.Open(path)
		if err != nil {
			temporary.Close()
			return err
		}
		if err := writeTarFile(tarWriter, entry.Name, file, entry.Size); err != nil {
			file.Close()
			temporary.Close()
			return err
		}
		file.Close()
	}
	if err := tarWriter.Close(); err != nil {
		temporary.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, output)
}

func writeTarBytes(writer *tar.Writer, name string, data []byte) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func writeTarFile(writer *tar.Writer, name string, file *os.File, size int64) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size}); err != nil {
		return err
	}
	_, err := io.CopyN(writer, file, size)
	return err
}

func safeArchiveName(name string) bool {
	clean := filepath.ToSlash(filepath.Clean(name))
	return name == clean && name != "" && name != "." && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && !strings.Contains(name, "/../") && !strings.ContainsRune(name, '\x00')
}

func atomicReplace(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".restore-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
