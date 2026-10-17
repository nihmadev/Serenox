package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Restore starts every deterministically ordered active installed version. It
// never changes package activation: one failed process is recorded as failed
// while the remaining plugins continue. Calling Restore again leaves matching
// running processes untouched and retries only non-running entries.
func (c *Controller) Restore(parent context.Context) error {
	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}
	installed, err := c.manager.List(opCtx)
	if err != nil {
		return fmt.Errorf("list installed plugins for restore: %w", err)
	}
	sort.Slice(installed, func(i, j int) bool { return installed[i].ID < installed[j].ID })
	var joined error
	for _, plugin := range installed {
		if err := opCtx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		if plugin.ActiveVersion == "" {
			continue
		}
		receipt, found := activeReceipt(plugin)
		if !found {
			spec := LaunchSpec{ID: plugin.ID, Name: plugin.Name, Version: plugin.ActiveVersion, Category: plugin.Category}
			if c.get(plugin.ID) == nil && c.size() >= c.maxPlugins {
				joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
				continue
			}
			c.markFailed(plugin.ID, spec, errors.New("active receipt is missing"))
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w: active receipt is missing", plugin.ID, ErrInconsistentInstall))
			continue
		}
		spec, resolveErr := resolveLaunchSpec(installed, receipt)
		if resolveErr != nil {
			if c.get(plugin.ID) == nil && c.size() >= c.maxPlugins {
				joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
				continue
			}
			c.markFailed(plugin.ID, LaunchSpec{
				ID: plugin.ID, Name: plugin.Name, Version: plugin.ActiveVersion, Category: plugin.Category,
				ManifestDigest: receipt.ManifestDigest,
			}, resolveErr)
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, resolveErr))
			continue
		}
		old := c.get(plugin.ID)
		if old != nil && c.isRunning(old) && samePackage(old.spec, spec) {
			continue
		}
		if old == nil && c.size() >= c.maxPlugins {
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
			continue
		}
		if old != nil && c.hasProcess(old) {
			c.setStateIfCurrent(plugin.ID, old, stateStopping)
			if stopErr := c.stopEntry(opCtx, old); stopErr != nil {
				c.setStateIfCurrent(plugin.ID, old, stateFailed)
				joined = errors.Join(joined, fmt.Errorf("restore %s: stop previous process: %w", plugin.ID, stopErr))
				continue
			}
		}
		if _, startErr := c.startAndStore(opCtx, spec); startErr != nil {
			c.markFailed(plugin.ID, spec, startErr)
			joined = errors.Join(joined, fmt.Errorf("restore %s@%s: %w", plugin.ID, spec.Version, startErr))
		}
	}
	return joined
}
