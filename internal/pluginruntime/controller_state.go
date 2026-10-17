package pluginruntime

import (
	"context"
	"time"
)

func (c *Controller) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

func (c *Controller) checkOpen() error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (c *Controller) generation() uint64 {
	c.mu.Lock()
	c.nextID++
	result := c.nextID
	c.mu.Unlock()
	return result
}

func (c *Controller) size() int {
	c.mu.RLock()
	result := len(c.items)
	c.mu.RUnlock()
	return result
}

func (c *Controller) get(id string) *entry {
	c.mu.RLock()
	result := c.items[id]
	c.mu.RUnlock()
	return result
}

func (c *Controller) isRunning(expected *entry) bool {
	c.mu.RLock()
	running := c.items[expected.spec.ID] == expected && expected.state == stateRunning && expected.process != nil
	c.mu.RUnlock()
	return running
}

func (c *Controller) hasProcess(expected *entry) bool {
	c.mu.RLock()
	hasProcess := expected.process != nil
	c.mu.RUnlock()
	return hasProcess
}

func (c *Controller) put(id string, current *entry) {
	c.mu.Lock()
	c.items[id] = current
	c.mu.Unlock()
}

func (c *Controller) delete(id string) {
	c.mu.Lock()
	delete(c.items, id)
	c.mu.Unlock()
}

func (c *Controller) deleteIfCurrent(id string, expected *entry) {
	c.mu.Lock()
	if c.items[id] == expected {
		delete(c.items, id)
	}
	c.mu.Unlock()
}

func (c *Controller) setStateIfCurrent(id string, expected *entry, state runtimeState) {
	c.mu.Lock()
	if c.items[id] == expected {
		expected.state = state
	}
	c.mu.Unlock()
}

func (c *Controller) markFailed(id string, spec LaunchSpec, causes ...error) {
	lastError := ""
	for _, cause := range causes {
		if cause != nil {
			lastError = cause.Error()
		}
	}
	c.put(id, &entry{spec: cloneLaunchSpec(spec), state: stateFailed, generation: c.generation(), lastError: lastError})
}
