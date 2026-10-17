package pluginpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

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
	if m.runtimeValidator != nil {
		if err := m.runtimeValidator(ctx, manifest); err != nil {
			return Receipt{}, fmt.Errorf("validate %s runtime contract: %w", manifest.Category, err)
		}
	}
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
		Entrypoint: verified.Entrypoint,
		Protocol:   manifest.Protocol,
		SDK:        manifest.SDK, Sidecar: manifest.Sidecar, Resources: manifest.Resources,
		Dependencies:       manifest.Dependencies,
		Network:            manifest.Network,
		Capabilities:       append([]string(nil), manifest.Capabilities...),
		Permissions:        append([]string(nil), manifest.Permissions...),
		EventPublications:  append([]pluginv1.EventPublication(nil), manifest.EventPublications...),
		EventSubscriptions: append([]pluginv1.EventSubscription(nil), manifest.EventSubscriptions...),
		Signed:             verified.Signed, SignerKeyID: verified.SignerKeyID,
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
