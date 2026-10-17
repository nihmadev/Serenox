package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Load verifies and installs a .srnx through PackageManager, then replaces
// the running process. Failed updates restore both the previous active pointer
// and, when it was running, the previous process.
func (c *Controller) Load(parent context.Context, packagePath string) error {
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(packagePath)), ".srnx") {
		return ErrInvalidPackageExtension
	}
	packagePath = strings.TrimSpace(packagePath)

	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}

	before, err := c.manager.List(opCtx)
	if err != nil {
		return fmt.Errorf("list packages before install: %w", err)
	}
	receipt, err := c.manager.Install(opCtx, packagePath)
	if err != nil {
		return fmt.Errorf("verify and install %s: %w", filepath.Base(packagePath), err)
	}
	after, err := c.manager.List(opCtx)
	if err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("list packages after install: %w", err), rollbackErr)
	}
	newSpec, err := resolveLaunchSpec(after, receipt)
	if err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(err, rollbackErr)
	}

	previousSpec, hadPreviousActive := resolveActiveSpec(before, receipt.ID)
	old := c.get(receipt.ID)
	if old == nil && c.size() >= c.maxPlugins {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("%w: maximum is %d", ErrCapacity, c.maxPlugins), rollbackErr)
	}
	if old != nil && hadPreviousActive &&
		(old.spec.Version != previousSpec.Version || old.spec.ManifestDigest != previousSpec.ManifestDigest) {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("%w: runtime %s@%s differs from active package %s@%s",
			ErrInconsistentInstall, old.spec.ID, old.spec.Version, previousSpec.ID, previousSpec.Version), rollbackErr)
	}
	if old != nil && c.isRunning(old) && samePackage(old.spec, newSpec) {
		return nil
	}
	if err := opCtx.Err(); err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(err, rollbackErr)
	}

	oldWasRunning := old != nil && c.isRunning(old)
	oldHadProcess := old != nil && c.hasProcess(old)
	if oldHadProcess {
		c.setStateIfCurrent(receipt.ID, old, stateStopping)
		if stopErr := c.stopEntry(opCtx, old); stopErr != nil {
			rollbackErr := c.restorePackage(before, receipt)
			c.setStateIfCurrent(receipt.ID, old, stateFailed)
			return errors.Join(fmt.Errorf("stop previous plugin process: %w", stopErr), rollbackErr)
		}
	}

	started, startErr := c.startAndStore(opCtx, newSpec)
	if startErr == nil {
		_ = started
		return nil
	}
	rollbackErr := c.restorePackage(before, receipt)
	if rollbackErr != nil {
		c.markFailed(receipt.ID, newSpec, startErr, rollbackErr)
		return errors.Join(fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr), rollbackErr)
	}

	if oldWasRunning {
		restartCtx, restartCancel := context.WithTimeout(context.Background(), c.rollbackTimeout)
		defer restartCancel()
		if _, restartErr := c.startAndStore(restartCtx, old.spec); restartErr != nil {
			c.markFailed(receipt.ID, old.spec, restartErr)
			return errors.Join(
				fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr),
				fmt.Errorf("restart previous %s@%s: %w", old.spec.ID, old.spec.Version, restartErr),
			)
		}
	} else if old != nil {
		c.put(receipt.ID, old)
	} else {
		c.delete(receipt.ID)
	}
	return fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr)
}
