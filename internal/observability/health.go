package observability

import (
	"errors"
	"sort"
	"sync"
	"time"
)

type HealthState string

const (
	HealthStarting HealthState = "starting"
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthStopped  HealthState = "stopped"
)

type HealthStatus struct {
	Component string      `json:"component"`
	State     HealthState `json:"state"`
	Detail    string      `json:"detail,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// Health tracks a bounded set of known components.
type Health struct {
	mu         sync.RWMutex
	max        int
	components map[string]HealthStatus
}

func NewHealth(maxComponents int) (*Health, error) {
	if maxComponents <= 0 {
		return nil, errors.New("max health components must be positive")
	}
	return &Health{max: maxComponents, components: make(map[string]HealthStatus, maxComponents)}, nil
}

func (h *Health) Set(component string, state HealthState, detail string) error {
	if component == "" || len(component) > 128 || len(detail) > 1024 {
		return errors.New("invalid health status")
	}
	if state != HealthStarting && state != HealthReady && state != HealthDegraded && state != HealthStopped {
		return errors.New("invalid health state")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.components[component]; !exists && len(h.components) >= h.max {
		return errors.New("health component limit reached")
	}
	h.components[component] = HealthStatus{
		Component: component,
		State:     state,
		Detail:    detail,
		UpdatedAt: time.Now().UTC(),
	}
	return nil
}

func (h *Health) Snapshot() []HealthStatus {
	h.mu.RLock()
	result := make([]HealthStatus, 0, len(h.components))
	for _, status := range h.components {
		result = append(result, status)
	}
	h.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Component < result[j].Component })
	return result
}
