package pluginpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func (m *Manager) List(ctx context.Context) ([]InstalledPlugin, error) {
	release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.listUnlocked()
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
