package pluginruntime

import (
	"context"
	"errors"
	"fmt"
)

func (c *Controller) startAndStore(startCtx context.Context, spec LaunchSpec) (*entry, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	current := &entry{spec: cloneLaunchSpec(spec), state: stateStarting, generation: c.generation()}
	c.put(spec.ID, current)

	runCtx, runCancel := context.WithCancel(c.ctx)
	stopForward := context.AfterFunc(startCtx, runCancel)
	process, err := c.factory.Start(runCtx, cloneLaunchSpec(current.spec))
	forwardRemoved := stopForward()
	if err == nil && (!forwardRemoved || startCtx.Err() != nil) {
		err = startCtx.Err()
		if err == nil {
			err = context.Canceled
		}
	}
	if err == nil && process == nil {
		err = errors.New("plugin process factory returned a nil process")
	}
	if err != nil {
		runCancel()
		if process != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
			_ = process.Stop(cleanupCtx)
			cleanupCancel()
		}
		c.deleteIfCurrent(spec.ID, current)
		return nil, err
	}

	metadata := clonePluginMetadata(process.Metadata())
	hookReady := false
	if metadataErr := metadata.Validate(); metadataErr != nil {
		err = fmt.Errorf("validate process metadata: %w", metadataErr)
	} else if metadata.ID != current.spec.ID || metadata.Version != current.spec.Version {
		err = fmt.Errorf("process metadata %s@%s does not match package %s@%s",
			metadata.ID, metadata.Version, current.spec.ID, current.spec.Version)
	} else if !capabilitiesMatch(current.spec.Capabilities, metadata.Capabilities) {
		err = fmt.Errorf("process capabilities %v do not match package declaration %v", metadata.Capabilities, current.spec.Capabilities)
	} else if c.hooks.OnReady != nil {
		err = c.hooks.OnReady(metadata, permissionCheckedDispatch(process, current.spec.Permissions))
		if err != nil {
			err = fmt.Errorf("register ready plugin: %w", err)
		} else {
			hookReady = true
		}
	}
	if err == nil {
		err = startCtx.Err()
	}
	if err != nil {
		runCancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
		_ = process.Stop(cleanupCtx)
		cleanupCancel()
		if hookReady && c.hooks.OnStopped != nil {
			c.hooks.OnStopped(current.spec.ID)
		}
		c.deleteIfCurrent(spec.ID, current)
		return nil, err
	}

	c.mu.Lock()
	if c.items[spec.ID] != current {
		c.mu.Unlock()
		runCancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
		_ = process.Stop(cleanupCtx)
		cleanupCancel()
		return nil, errors.New("plugin runtime state changed during startup")
	}
	current.process = process
	current.cancel = runCancel
	current.monitorDone = make(chan struct{})
	current.state = stateRunning
	current.registered = true
	c.mu.Unlock()
	go c.monitor(runCtx, current)
	return current, nil
}

func (c *Controller) monitor(runCtx context.Context, watched *entry) {
	err := watched.process.Wait(runCtx)
	notify := false
	c.mu.Lock()
	current := c.items[watched.spec.ID]
	if current == watched && current.generation == watched.generation {
		wasStopping := current.state == stateStopping
		if wasStopping || errors.Is(err, context.Canceled) {
			current.state = stateStopped
		} else if err != nil {
			current.state = stateFailed
		} else {
			current.state = stateStopped
		}
		if !wasStopping && current.registered {
			current.registered = false
			notify = true
		}
	}
	c.mu.Unlock()
	if notify && c.hooks.OnStopped != nil {
		c.hooks.OnStopped(watched.spec.ID)
	}
	close(watched.monitorDone)
}

func (c *Controller) stopEntry(parent context.Context, current *entry) error {
	c.mu.RLock()
	process := current.process
	runCancel := current.cancel
	monitorDone := current.monitorDone
	c.mu.RUnlock()
	if process == nil {
		c.notifyStopped(current)
		return nil
	}
	if runCancel != nil {
		runCancel()
	}
	stopCtx, cancel := boundedContext(parent, c.stopTimeout)
	defer cancel()
	stopErr := process.Stop(stopCtx)
	if monitorDone != nil {
		select {
		case <-monitorDone:
		case <-stopCtx.Done():
			return errors.Join(stopErr, stopCtx.Err())
		}
	}
	c.notifyStopped(current)
	return stopErr
}

func (c *Controller) notifyStopped(current *entry) {
	notify := false
	c.mu.Lock()
	if current.registered {
		current.registered = false
		notify = true
	}
	c.mu.Unlock()
	if notify && c.hooks.OnStopped != nil {
		c.hooks.OnStopped(current.spec.ID)
	}
}
