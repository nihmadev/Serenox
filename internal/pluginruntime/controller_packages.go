package pluginruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (c *Controller) restorePackage(before []pluginpackage.InstalledPlugin, installed pluginpackage.Receipt) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), c.rollbackTimeout)
	defer cancel()
	previous, exists := findInstalled(before, installed.ID)
	if !exists || previous.ActiveVersion == "" {
		if err := c.manager.Uninstall(rollbackCtx, installed.ID, installed.Version); err != nil {
			return fmt.Errorf("remove failed installation %s@%s: %w", installed.ID, installed.Version, err)
		}
		return nil
	}
	if previous.ActiveVersion == installed.Version {
		return nil
	}
	if _, err := c.manager.Rollback(rollbackCtx, installed.ID, previous.ActiveVersion); err != nil {
		return fmt.Errorf("restore active version %s@%s: %w", installed.ID, previous.ActiveVersion, err)
	}
	return nil
}

func resolveLaunchSpec(installed []pluginpackage.InstalledPlugin, receipt pluginpackage.Receipt) (LaunchSpec, error) {
	plugin, exists := findInstalled(installed, receipt.ID)
	if !exists || plugin.ActiveVersion != receipt.Version {
		return LaunchSpec{}, fmt.Errorf("%w: %s@%s is not active after installation", ErrInconsistentInstall, receipt.ID, receipt.Version)
	}
	for _, version := range plugin.Versions {
		if version.Receipt.Version != receipt.Version || !version.Active {
			continue
		}
		if version.Receipt.ManifestDigest != receipt.ManifestDigest {
			return LaunchSpec{}, fmt.Errorf("%w: receipt digest changed for %s@%s", ErrInconsistentInstall, receipt.ID, receipt.Version)
		}
		entrypoint := filepath.Join(version.Path, filepath.FromSlash(receipt.Entrypoint.Path))
		relative, err := filepath.Rel(version.Path, entrypoint)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return LaunchSpec{}, fmt.Errorf("%w: entrypoint escapes installed version", ErrInconsistentInstall)
		}
		return LaunchSpec{
			ID: receipt.ID, Name: receipt.Name, Version: receipt.Version, Category: receipt.Category,
			VersionRoot: version.Path, Entrypoint: entrypoint, ManifestDigest: receipt.ManifestDigest,
			Module: receipt.Entrypoint.Module, Class: receipt.Entrypoint.Class,
			Capabilities:       append([]string(nil), receipt.Capabilities...),
			Permissions:        append([]string(nil), receipt.Permissions...),
			EventPublications:  append([]pluginv1.EventPublication(nil), receipt.EventPublications...),
			EventSubscriptions: append([]pluginv1.EventSubscription(nil), receipt.EventSubscriptions...),
			Network:            receipt.Network,
			Signed:             receipt.Signed, Trusted: receipt.Trusted, Protocol: receipt.Protocol, SDK: receipt.SDK, Sidecar: receipt.Sidecar,
			Resources: receipt.Resources, Dependencies: receipt.Dependencies,
		}, nil
	}
	return LaunchSpec{}, fmt.Errorf("%w: active version directory is missing for %s@%s", ErrInconsistentInstall, receipt.ID, receipt.Version)
}

func resolveActiveSpec(installed []pluginpackage.InstalledPlugin, pluginID string) (LaunchSpec, bool) {
	plugin, exists := findInstalled(installed, pluginID)
	if !exists || plugin.ActiveVersion == "" {
		return LaunchSpec{}, false
	}
	for _, version := range plugin.Versions {
		if version.Receipt.Version == plugin.ActiveVersion && version.Active {
			spec, err := resolveLaunchSpec(installed, version.Receipt)
			return spec, err == nil
		}
	}
	return LaunchSpec{}, false
}

func findInstalled(installed []pluginpackage.InstalledPlugin, pluginID string) (pluginpackage.InstalledPlugin, bool) {
	for _, plugin := range installed {
		if plugin.ID == pluginID {
			return plugin, true
		}
	}
	return pluginpackage.InstalledPlugin{}, false
}

func activeReceipt(plugin pluginpackage.InstalledPlugin) (pluginpackage.Receipt, bool) {
	for _, version := range plugin.Versions {
		if version.Active && version.Receipt.Version == plugin.ActiveVersion {
			return version.Receipt, true
		}
	}
	return pluginpackage.Receipt{}, false
}

func samePackage(left, right LaunchSpec) bool {
	return left.ID == right.ID && left.Version == right.Version && left.ManifestDigest == right.ManifestDigest
}

func cloneLaunchSpec(spec LaunchSpec) LaunchSpec {
	result := spec
	result.Capabilities = append([]string(nil), spec.Capabilities...)
	result.Permissions = append([]string(nil), spec.Permissions...)
	result.EventPublications = append([]pluginv1.EventPublication(nil), spec.EventPublications...)
	result.EventSubscriptions = append([]pluginv1.EventSubscription(nil), spec.EventSubscriptions...)
	result.Network.Origins = append([]pluginpackage.NetworkOrigin(nil), spec.Network.Origins...)
	return result
}
