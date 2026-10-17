package pluginpackage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

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
