package pluginpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

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
