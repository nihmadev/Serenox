// Package eventbus implements the single-daemon Serenox Event Fabric.
package eventbus

import (
	"context"
	"errors"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrNotDeclared   = errors.New("eventbus: event is not declared")
	ErrPermission    = errors.New("eventbus: event permission denied")
	ErrQuota         = errors.New("eventbus: retained event quota exceeded")
	ErrFanout        = errors.New("eventbus: fan-out limit exceeded")
	ErrNotFound      = errors.New("eventbus: event or delivery not found")
	ErrStaleRevision = errors.New("eventbus: stale revision")
	ErrInvalidState  = errors.New("eventbus: invalid delivery state transition")
	ErrHopLimit      = errors.New("eventbus: causal hop limit exceeded")
)

type DeliveryState string

const (
	StatePending      DeliveryState = "pending"
	StateAdmitted     DeliveryState = "admitted"
	StateRunning      DeliveryState = "running"
	StateAcknowledged DeliveryState = "acknowledged"
	StateRetryWait    DeliveryState = "retry_wait"
	StateDeadLettered DeliveryState = "dead_lettered"
)

type Delivery struct {
	ID                string                     `json:"id"`
	PluginID          string                     `json:"plugin_id"`
	Subscription      protocol.EventSubscription `json:"subscription"`
	State             DeliveryState              `json:"state"`
	Attempt           int                        `json:"attempt"`
	Revision          uint64                     `json:"revision"`
	NextAttemptUnixMS int64                      `json:"next_attempt_unix_ms,omitempty"`
	StartedUnixMS     int64                      `json:"started_unix_ms,omitempty"`
	FinishedUnixMS    int64                      `json:"finished_unix_ms,omitempty"`
	LastError         string                     `json:"last_error,omitempty"`
}

type Record struct {
	SchemaVersion int                         `json:"schema_version"`
	Envelope      protocol.EventEnvelope      `json:"envelope"`
	Class         protocol.EventDeliveryClass `json:"delivery_class"`
	Deliveries    []Delivery                  `json:"deliveries"`
	Revision      uint64                      `json:"revision"`
	ExpiresUnixMS int64                       `json:"expires_unix_ms"`
}

type Store interface {
	Put(context.Context, Record, uint64) error
	Get(context.Context, string) (Record, bool, error)
	List(context.Context, int) ([]Record, error)
	Delete(context.Context, string, uint64) error
}

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type IDGenerator interface{ NewEventID() string }
type Metrics interface {
	Observe(name, eventType, pluginID, result string)
}
type noopMetrics struct{}

func (noopMetrics) Observe(string, string, string, string) {}

type Identity struct {
	PluginID  string
	AccountID int64
}

type Limits struct {
	MaxRetainedEvents       int
	MaxRetainedBytes        int64
	MaxFanout               int
	MaxHop                  int
	Retention               time.Duration
	MaxList                 int
	MaxTraceEvents          int
	MaxEventsPerMinute      int
	MaxBytesPerMinute       int64
	MaxInFlightPerPublisher int
}

func DefaultLimits() Limits {
	return Limits{MaxRetainedEvents: 4096, MaxRetainedBytes: 64 << 20, MaxFanout: protocol.MaxEventFanout, MaxHop: protocol.MaxEventHop, Retention: 7 * 24 * time.Hour, MaxList: 100, MaxTraceEvents: 64, MaxEventsPerMinute: 600, MaxBytesPerMinute: 8 << 20, MaxInFlightPerPublisher: 256}
}

type Registration struct {
	PluginID      string
	AccountID     int64
	Capabilities  []protocol.Capability
	Permissions   []string
	Publications  []protocol.EventPublication
	Subscriptions []protocol.EventSubscription
}

type Stats struct {
	RetainedEvents                                          int
	RetainedBytes                                           int64
	Pending, Running, Acknowledged, RetryWait, DeadLettered int
}
