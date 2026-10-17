// Package modules owns module registration and lifecycle state transitions.
// Built-in and process-backed modules share this registry.
package modules

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

var (
	ErrDuplicate       = errors.New("module already registered")
	ErrNotFound        = errors.New("module not found")
	ErrCoreProtected   = errors.New("core module is protected")
	ErrCapacity        = errors.New("module capacity reached")
	ErrInvalidMetadata = errors.New("invalid module metadata")
	ErrDependency      = errors.New("module dependency error")
	// ErrSuspend lets Configure or Start keep a module installed without making
	// its handlers ready. This is the explicit counterpart of legacy SelfSuspend.
	ErrSuspend = errors.New("module suspended")
)

type State string

const (
	StateDiscovered State = "discovered"
	StateConfigured State = "configured"
	StateStarting   State = "starting"
	StateReady      State = "ready"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateFailed     State = "failed"
	StateSuspended  State = "suspended"
)

type Metadata struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Core         bool     `json:"core"`
}

type Services struct {
	Values map[string]any
}

// Module hooks are synchronous from the registry's perspective and receive a
// deadline-bound context from the application owner.
type Module interface {
	Metadata() Metadata
	Configure(context.Context, Services) error
	Start(context.Context) error
	Stop(context.Context) error
}

type Status struct {
	Metadata  Metadata  `json:"metadata"`
	State     State     `json:"state"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type entry struct {
	module Module
	status Status
}

type Registry struct {
	mu       sync.RWMutex
	max      int
	entries  map[string]*entry
	order    []string
	services Services
}

func NewRegistry(maxModules int, services Services) (*Registry, error) {
	if maxModules <= 0 {
		return nil, errors.New("max modules must be positive")
	}
	if services.Values == nil {
		services.Values = map[string]any{}
	}
	return &Registry{
		max: maxModules, entries: make(map[string]*entry, maxModules), services: services,
	}, nil
}

func (r *Registry) Register(module Module) error {
	if module == nil {
		return ErrInvalidMetadata
	}
	metadata := cloneMetadata(module.Metadata())
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[metadata.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, metadata.ID)
	}
	if len(r.entries) >= r.max {
		return ErrCapacity
	}
	now := time.Now().UTC()
	r.entries[metadata.ID] = &entry{module: module, status: Status{Metadata: metadata, State: StateDiscovered, UpdatedAt: now}}
	r.order = append(r.order, metadata.ID)
	return nil
}

// ConfigureAll configures modules in dependency order. Suspended modules remain
// registered but cannot satisfy dependencies.
func (r *Registry) ConfigureAll(ctx context.Context) error {
	order, err := r.dependencyOrder()
	if err != nil {
		return err
	}
	for _, id := range order {
		module, state, err := r.moduleFor(id)
		if err != nil {
			return err
		}
		if state != StateDiscovered && state != StateStopped {
			continue
		}
		if dep := r.unreadyDependency(id, StateConfigured); dep != "" {
			r.setState(id, StateFailed, "dependency not configured: "+dep)
			return fmt.Errorf("%w: %s requires %s", ErrDependency, id, dep)
		}
		if err := module.Configure(ctx, r.services); err != nil {
			if errors.Is(err, ErrSuspend) {
				r.setState(id, StateSuspended, err.Error())
				continue
			}
			r.setState(id, StateFailed, err.Error())
			return fmt.Errorf("configure module %s: %w", id, err)
		}
		r.setState(id, StateConfigured, "")
	}
	return nil
}

// StartAll starts configured modules in dependency order. A failure stops
// modules started by this call in reverse order before returning.
func (r *Registry) StartAll(ctx context.Context) error {
	order, err := r.dependencyOrder()
	if err != nil {
		return err
	}
	started := make([]string, 0, len(order))
	for _, id := range order {
		module, state, err := r.moduleFor(id)
		if err != nil {
			return err
		}
		if state == StateSuspended {
			continue
		}
		if state != StateConfigured {
			err = fmt.Errorf("module %s is %s, expected configured", id, state)
			r.rollback(ctx, started)
			return err
		}
		if dep := r.unreadyDependency(id, StateReady); dep != "" {
			r.setState(id, StateFailed, "dependency not ready: "+dep)
			r.rollback(ctx, started)
			return fmt.Errorf("%w: %s requires ready %s", ErrDependency, id, dep)
		}
		r.setState(id, StateStarting, "")
		if err := module.Start(ctx); err != nil {
			if errors.Is(err, ErrSuspend) {
				r.setState(id, StateSuspended, err.Error())
				continue
			}
			r.setState(id, StateFailed, err.Error())
			r.rollback(ctx, started)
			return fmt.Errorf("start module %s: %w", id, err)
		}
		r.setState(id, StateReady, "")
		started = append(started, id)
	}
	return nil
}

func (r *Registry) StopAll(ctx context.Context) error {
	order, orderErr := r.dependencyOrder()
	if orderErr != nil {
		return orderErr
	}
	var joined error
	for index := len(order) - 1; index >= 0; index-- {
		id := order[index]
		module, state, err := r.moduleFor(id)
		if err != nil || (state != StateReady && state != StateStarting && state != StateFailed) {
			continue
		}
		r.setState(id, StateStopping, "")
		if err := module.Stop(ctx); err != nil {
			r.setState(id, StateFailed, err.Error())
			joined = errors.Join(joined, fmt.Errorf("stop module %s: %w", id, err))
			continue
		}
		r.setState(id, StateStopped, "")
	}
	return joined
}

func (r *Registry) Unregister(ctx context.Context, id string) error {
	module, state, err := r.moduleFor(id)
	if err != nil {
		return err
	}
	if module.Metadata().Core {
		return fmt.Errorf("%w: %s", ErrCoreProtected, id)
	}
	if state == StateReady || state == StateStarting || state == StateFailed {
		r.setState(id, StateStopping, "")
		if err := module.Stop(ctx); err != nil {
			r.setState(id, StateFailed, err.Error())
			return fmt.Errorf("stop module %s: %w", id, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
	for index, current := range r.order {
		if current == id {
			r.order = append(r.order[:index], r.order[index+1:]...)
			break
		}
	}
	return nil
}

func (r *Registry) Lookup(id string) (Module, Status, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	current, ok := r.entries[id]
	if !ok {
		return nil, Status{}, false
	}
	status := current.status
	status.Metadata = cloneMetadata(status.Metadata)
	return current.module, status, true
}

func (r *Registry) Snapshot() []Status {
	r.mu.RLock()
	statuses := make([]Status, 0, len(r.entries))
	for _, id := range r.order {
		status := r.entries[id].status
		status.Metadata = cloneMetadata(status.Metadata)
		statuses = append(statuses, status)
	}
	r.mu.RUnlock()
	return statuses
}

func (r *Registry) dependencyOrder() ([]string, error) {
	r.mu.RLock()
	metadata := make(map[string]Metadata, len(r.entries))
	orderHint := append([]string(nil), r.order...)
	for id, current := range r.entries {
		metadata[id] = cloneMetadata(current.status.Metadata)
	}
	r.mu.RUnlock()
	orderIndex := make(map[string]int, len(orderHint))
	for index, id := range orderHint {
		orderIndex[id] = index
	}
	visited := make(map[string]uint8, len(metadata))
	result := make([]string, 0, len(metadata))
	var visit func(string) error
	visit = func(id string) error {
		switch visited[id] {
		case 1:
			return fmt.Errorf("%w: cycle at %s", ErrDependency, id)
		case 2:
			return nil
		}
		current, ok := metadata[id]
		if !ok {
			return fmt.Errorf("%w: missing %s", ErrDependency, id)
		}
		visited[id] = 1
		dependencies := append([]string(nil), current.Dependencies...)
		sort.SliceStable(dependencies, func(i, j int) bool { return orderIndex[dependencies[i]] < orderIndex[dependencies[j]] })
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visited[id] = 2
		result = append(result, id)
		return nil
	}
	for _, id := range orderHint {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *Registry) unreadyDependency(id string, wanted State) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, dependency := range r.entries[id].status.Metadata.Dependencies {
		current, ok := r.entries[dependency]
		if !ok || current.status.State != wanted {
			return dependency
		}
	}
	return ""
}

func (r *Registry) moduleFor(id string) (Module, State, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	current, ok := r.entries[id]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return current.module, current.status.State, nil
}

func (r *Registry) setState(id string, state State, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.entries[id]; current != nil {
		current.status.State = state
		current.status.Error = message
		current.status.UpdatedAt = time.Now().UTC()
	}
}

func (r *Registry) rollback(ctx context.Context, started []string) {
	for index := len(started) - 1; index >= 0; index-- {
		id := started[index]
		module, _, err := r.moduleFor(id)
		if err != nil {
			continue
		}
		r.setState(id, StateStopping, "")
		if err := module.Stop(ctx); err != nil {
			r.setState(id, StateFailed, err.Error())
		} else {
			r.setState(id, StateStopped, "")
		}
	}
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func validateMetadata(metadata Metadata) error {
	if !validID.MatchString(metadata.ID) || metadata.Name == "" || len(metadata.Name) > 128 ||
		metadata.Version == "" || len(metadata.Version) > 64 || len(metadata.Description) > 2048 ||
		len(metadata.Dependencies) > 32 {
		return ErrInvalidMetadata
	}
	seen := make(map[string]struct{}, len(metadata.Dependencies))
	for _, dependency := range metadata.Dependencies {
		if !validID.MatchString(dependency) || dependency == metadata.ID {
			return ErrInvalidMetadata
		}
		if _, duplicate := seen[dependency]; duplicate {
			return ErrInvalidMetadata
		}
		seen[dependency] = struct{}{}
	}
	return nil
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.Dependencies = append([]string(nil), metadata.Dependencies...)
	return metadata
}
