package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (f *Fabric) Claim(ctx context.Context, eventID, deliveryID string) (protocol.EventDelivery, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, found, err := f.store.Get(ctx, eventID)
	if err != nil {
		return protocol.EventDelivery{}, 0, err
	}
	if !found {
		return protocol.EventDelivery{}, 0, ErrNotFound
	}
	index := deliveryIndex(record, deliveryID)
	if index < 0 {
		return protocol.EventDelivery{}, 0, ErrNotFound
	}
	delivery := &record.Deliveries[index]
	registration, active := f.registrations[registrationKey(delivery.PluginID, record.Envelope.AccountID)]
	if !active || !hasSubscription(registration, delivery.Subscription) {
		return protocol.EventDelivery{}, 0, ErrPermission
	}
	now := f.clock.Now().UnixMilli()
	if delivery.State != StatePending && (delivery.State != StateRetryWait || delivery.NextAttemptUnixMS > now) {
		return protocol.EventDelivery{}, 0, ErrInvalidState
	}
	delivery.State = StateRunning
	delivery.Attempt++
	delivery.StartedUnixMS = now
	delivery.Revision++
	record.Envelope.Attempt = delivery.Attempt
	expected := record.Revision
	if err := f.store.Put(ctx, record, expected); err != nil {
		return protocol.EventDelivery{}, 0, err
	}
	f.metrics.Observe("running", record.Envelope.Type, delivery.PluginID, "admitted")
	return protocol.EventDelivery{SubscriptionID: delivery.Subscription.HandlerID, Envelope: record.Envelope, PreviousError: delivery.LastError}, delivery.Revision, nil
}

func (f *Fabric) Complete(ctx context.Context, eventID, deliveryID string, revision uint64, result protocol.EventHandlerResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	record, found, err := f.store.Get(ctx, eventID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	index := deliveryIndex(record, deliveryID)
	if index < 0 {
		return ErrNotFound
	}
	delivery := &record.Deliveries[index]
	if delivery.State != StateRunning {
		return ErrInvalidState
	}
	if delivery.Revision != revision {
		return ErrStaleRevision
	}
	now := f.clock.Now().UnixMilli()
	delivery.FinishedUnixMS = now
	delivery.Revision++
	delivery.LastError = boundedError(result.Code, result.Message)
	switch result.Outcome {
	case protocol.EventAcknowledged:
		delivery.State = StateAcknowledged
		delivery.LastError = ""
	case protocol.EventTerminal, protocol.EventAmbiguous:
		delivery.State = StateDeadLettered
	case protocol.EventRetryable:
		if record.Class == protocol.EventEphemeral || delivery.Attempt >= delivery.Subscription.Retry.MaxAttempts {
			delivery.State = StateDeadLettered
		} else {
			delivery.State = StateRetryWait
			delivery.NextAttemptUnixMS = now + backoffMS(delivery.Subscription.Retry, delivery.Attempt, eventID)
		}
	}
	expected := record.Revision
	if err := f.store.Put(ctx, record, expected); err != nil {
		return err
	}
	f.metrics.Observe(string(delivery.State), record.Envelope.Type, delivery.PluginID, string(result.Outcome))
	return nil
}

func (f *Fabric) Recover(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil {
		return 0, err
	}
	now := f.clock.Now().UnixMilli()
	recovered := 0
	for _, record := range records {
		changed := false
		for i := range record.Deliveries {
			delivery := &record.Deliveries[i]
			if delivery.State == StateRunning || delivery.State == StateAdmitted {
				if record.Class == protocol.EventEphemeral {
					delivery.State = StateDeadLettered
					delivery.LastError = "host_interrupted_ephemeral"
				} else {
					delivery.State = StateRetryWait
					delivery.LastError = "host_interrupted"
					delivery.NextAttemptUnixMS = now
				}
				delivery.Revision++
				changed = true
				recovered++
			}
		}
		if changed {
			if err := f.store.Put(ctx, record, record.Revision); err != nil {
				return recovered, err
			}
		}
	}
	return recovered, nil
}

func (f *Fabric) Replay(ctx context.Context, eventID, deliveryID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, found, err := f.store.Get(ctx, eventID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	index := deliveryIndex(record, deliveryID)
	if index < 0 {
		return ErrNotFound
	}
	delivery := &record.Deliveries[index]
	if delivery.State != StateDeadLettered || !delivery.Subscription.AcceptReplay {
		return ErrInvalidState
	}
	reg, active := f.registrations[registrationKey(delivery.PluginID, record.Envelope.AccountID)]
	if !active || !hasSubscription(reg, delivery.Subscription) {
		return ErrPermission
	}
	delivery.State = StatePending
	delivery.NextAttemptUnixMS = 0
	delivery.FinishedUnixMS = 0
	delivery.Revision++
	return f.store.Put(ctx, record, record.Revision)
}

func (f *Fabric) Discard(ctx context.Context, eventID string, revision uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store.Delete(ctx, eventID, revision)
}

func (f *Fabric) Expire(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil {
		return 0, err
	}
	now := f.clock.Now().UnixMilli()
	count := 0
	for _, record := range records {
		if record.ExpiresUnixMS <= now {
			if err := f.store.Delete(ctx, record.Envelope.ID, record.Revision); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func (f *Fabric) List(ctx context.Context, state DeliveryState, limit int) ([]Record, error) {
	if limit < 1 || limit > f.limits.MaxList {
		return nil, ErrQuota
	}
	if state != "" && state != StatePending && state != StateAdmitted && state != StateRunning && state != StateAcknowledged && state != StateRetryWait && state != StateDeadLettered {
		return nil, ErrInvalidState
	}
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, limit)
	for _, record := range records {
		if state == "" || recordHasState(record, state) {
			result = append(result, record)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// Ready returns bounded runnable work after scanning the bounded retained
// store. Operator pagination therefore cannot stall later records.
func (f *Fabric) Ready(ctx context.Context, limit int) ([]Record, error) {
	if limit < 1 || limit > f.limits.MaxList {
		return nil, ErrQuota
	}
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, limit)
	for _, record := range records {
		if recordHasState(record, StatePending) || recordHasState(record, StateRetryWait) {
			result = append(result, record)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (f *Fabric) Status(ctx context.Context) (Stats, error) {
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{RetainedEvents: len(records)}
	for _, record := range records {
		encoded, _ := json.Marshal(record)
		stats.RetainedBytes += int64(len(encoded))
		for _, delivery := range record.Deliveries {
			switch delivery.State {
			case StatePending:
				stats.Pending++
			case StateRunning, StateAdmitted:
				stats.Running++
			case StateAcknowledged:
				stats.Acknowledged++
			case StateRetryWait:
				stats.RetryWait++
			case StateDeadLettered:
				stats.DeadLettered++
			}
		}
	}
	return stats, nil
}

func deliveryIndex(record Record, id string) int {
	for i := range record.Deliveries {
		if record.Deliveries[i].ID == id {
			return i
		}
	}
	return -1
}
func hasSubscription(reg Registration, expected protocol.EventSubscription) bool {
	for _, current := range reg.Subscriptions {
		if current.HandlerID == expected.HandlerID && current.Type == expected.Type && current.Version == expected.Version {
			return true
		}
	}
	return false
}
func recordHasState(record Record, state DeliveryState) bool {
	for _, delivery := range record.Deliveries {
		if delivery.State == state {
			return true
		}
	}
	return false
}
func boundedError(code, message string) string {
	value := code
	if message != "" {
		value += ": " + message
	}
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}
func backoffMS(policy protocol.EventRetryPolicy, attempt int, eventID string) int64 {
	delay := policy.InitialDelayMS
	for i := 1; i < attempt && delay < policy.MaxDelayMS; i++ {
		if delay > policy.MaxDelayMS/2 {
			delay = policy.MaxDelayMS
		} else {
			delay *= 2
		}
	}
	if delay > policy.MaxDelayMS {
		delay = policy.MaxDelayMS
	}
	var hash uint64
	for _, b := range []byte(eventID) {
		hash = hash*33 + uint64(b)
	}
	jitter := int64(hash%21) - 10
	return delay + delay*jitter/100
}

var _ = fmt.Sprintf
var _ = time.Second
