package pluginpackage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
