package pluginpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
)

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
