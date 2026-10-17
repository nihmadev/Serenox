package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nihmadev/Serenox/internal/builtin"
)

// Unload stops a process and forgets only its runtime state. Installed package
// versions and their active pointer remain untouched.
func (c *Controller) Unload(parent context.Context, pluginID string) error {
	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}
	pluginID = strings.TrimSpace(pluginID)
	current := c.get(pluginID)
	if current == nil {
		return fmt.Errorf("%w: %s", ErrNotLoaded, pluginID)
	}
	if !c.hasProcess(current) {
		c.delete(pluginID)
		return nil
	}
	c.setStateIfCurrent(pluginID, current, stateStopping)
	if err := c.stopEntry(opCtx, current); err != nil {
		c.setStateIfCurrent(pluginID, current, stateFailed)
		return fmt.Errorf("stop plugin %s: %w", pluginID, err)
	}
	c.deleteIfCurrent(pluginID, current)
	return nil
}

// List returns a stable snapshot ordered by plugin ID.
func (c *Controller) List() []builtin.PluginStatus {
	return c.diagnosticsSnapshot(false)
}

// Diagnostics returns the same loaded-plugin view as List with supervised
// process and Python runtime details populated when available.
func (c *Controller) Diagnostics() []builtin.PluginStatus {
	return c.diagnosticsSnapshot(true)
}

func (c *Controller) diagnosticsSnapshot(withRuntime bool) []builtin.PluginStatus {
	c.mu.RLock()
	result := make([]builtin.PluginStatus, 0, len(c.items))
	for _, current := range c.items {
		status := builtin.PluginStatus{
			ID: current.spec.ID, Version: current.spec.Version,
			State: string(current.state), External: true, Category: string(current.spec.Category),
			PackageVersion: current.spec.Version, ProtocolVersion: "1.0",
			LastError: current.lastError,
		}
		if withRuntime {
			if process, ok := current.process.(interface{ RuntimeStatus() RuntimeStatus }); ok {
				runtimeStatus := process.RuntimeStatus()
				status.InterpreterPath = runtimeStatus.InterpreterPath
				status.InterpreterVersion = runtimeStatus.InterpreterVersion
				status.SidecarVersion = runtimeStatus.SidecarVersion
				status.SDKVersion = runtimeStatus.SDKVersion
				status.ProtocolVersion = runtimeStatus.ProtocolVersion
				status.ProcessState = runtimeStatus.ProcessState
				status.RestartCount = runtimeStatus.RestartCount
				status.CircuitState = runtimeStatus.CircuitState
				status.QueueDepth = runtimeStatus.QueueDepth
				status.WorkerCount = runtimeStatus.WorkerCount
				status.RSSBytes = runtimeStatus.RSSBytes
				status.LastError = runtimeStatus.LastError
			}
		}
		result = append(result, status)
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Close permanently prevents new work, cancels every owned process lifetime,
// stops each process sequentially, and waits for every bounded monitor.
func (c *Controller) Close(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()

	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.RLock()
	ids := make([]string, 0, len(c.items))
	for id := range c.items {
		ids = append(ids, id)
	}
	c.mu.RUnlock()
	sort.Strings(ids)

	var joined error
	for _, id := range ids {
		current := c.get(id)
		if current == nil {
			continue
		}
		if c.hasProcess(current) {
			c.setStateIfCurrent(id, current, stateStopping)
			if err := c.stopEntry(parent, current); err != nil {
				c.setStateIfCurrent(id, current, stateFailed)
				joined = errors.Join(joined, fmt.Errorf("stop plugin %s: %w", id, err))
				continue
			}
		}
		c.deleteIfCurrent(id, current)
	}
	return joined
}
