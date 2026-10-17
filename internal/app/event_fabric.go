package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nihmadev/Serenox/internal/eventbus"
	"github.com/nihmadev/Serenox/internal/executor"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type eventWork struct {
	request    pluginv1.DispatchRequest
	eventID    string
	deliveryID string
	revision   uint64
}

type fabricMetrics struct{ app *App }

func (m fabricMetrics) Observe(name, eventType, pluginID, result string) {
	_ = m.app.metrics.Add("serenox_event_fabric_"+name+"_total", map[string]string{"event_type": eventType, "plugin": pluginID, "result": result}, 1)
}

// HandleHostEvents is the authenticated host.events reverse-RPC boundary.
// Success means the event and complete initial fan-out were committed locally.
func (a *App) HandleHostEvents(ctx context.Context, pluginID string, accountID int64, request pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error) {
	if a.events == nil {
		return pluginv1.EventPublishResult{}, errors.New("event fabric is unavailable")
	}
	if accountID != a.SelfID() {
		return pluginv1.EventPublishResult{}, eventbus.ErrPermission
	}
	return a.events.Publish(ctx, eventbus.Identity{PluginID: pluginID, AccountID: accountID}, request)
}

func (a *App) EventStatus(ctx context.Context) (eventbus.Stats, error) { return a.events.Status(ctx) }
func (a *App) EventRecords(ctx context.Context, state eventbus.DeliveryState, limit int) ([]eventbus.Record, error) {
	return a.events.List(ctx, state, limit)
}
func (a *App) EventRecord(ctx context.Context, eventID string) (eventbus.Record, bool, error) {
	return a.events.Get(ctx, eventID)
}
func (a *App) ReplayEvent(ctx context.Context, eventID, deliveryID string) error {
	return a.events.Replay(ctx, eventID, deliveryID)
}
func (a *App) DiscardEvent(ctx context.Context, eventID string) error {
	record, found, err := a.events.Get(ctx, eventID)
	if err != nil {
		return err
	}
	if !found {
		return eventbus.ErrNotFound
	}
	return a.events.Discard(ctx, eventID, record.Revision)
}

func (a *App) runEventCoordinator() {
	defer a.workers.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	expiry := time.NewTicker(time.Minute)
	defer expiry.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if a.closing.Load() {
				return
			}
			a.admitEvents()
		case <-expiry.C:
			if _, err := a.events.Expire(a.ctx); err != nil {
				a.options.Logger.Error("event retention expiry failed", "error", err)
			}
		}
	}
}

func (a *App) admitEvents() {
	records, err := a.events.Ready(a.ctx, 100)
	if err != nil {
		a.options.Logger.Error("event fabric scan failed", "error", err)
		return
	}
	for _, record := range records {
		for _, delivery := range record.Deliveries {
			if delivery.State != eventbus.StatePending && delivery.State != eventbus.StateRetryWait {
				continue
			}
			if _, active := a.executor.Pool(delivery.PluginID); !active {
				continue
			}
			request, revision, claimErr := a.events.Claim(a.ctx, record.Envelope.ID, delivery.ID)
			if claimErr != nil {
				continue
			}
			work := eventWork{request: pluginv1.DispatchRequest{Event: &request}, eventID: record.Envelope.ID, deliveryID: delivery.ID, revision: revision}
			submitErr := a.executor.TrySubmit(delivery.PluginID, executor.Job{ID: record.Envelope.ID, HandlerID: delivery.Subscription.HandlerID, Kind: executor.KindEvent, Payload: work, Timeout: time.Duration(delivery.Subscription.MaxRuntimeMS) * time.Millisecond})
			if submitErr != nil {
				_ = a.events.Complete(a.ctx, record.Envelope.ID, delivery.ID, revision, pluginv1.EventHandlerResult{Outcome: pluginv1.EventRetryable, Code: "admission_failed", Message: submitErr.Error()})
			}
		}
	}
}

func (a *App) dispatchEvent(ctx context.Context, pluginID string, handler ExternalDispatch, work eventWork) error {
	response, err := handler(ctx, work.request)
	if err != nil {
		_ = a.events.Complete(context.Background(), work.eventID, work.deliveryID, work.revision, pluginv1.EventHandlerResult{Outcome: pluginv1.EventRetryable, Code: "dispatch_failed", Message: err.Error()})
		return err
	}
	if err := response.Validate(); err != nil {
		_ = a.events.Complete(context.Background(), work.eventID, work.deliveryID, work.revision, pluginv1.EventHandlerResult{Outcome: pluginv1.EventTerminal, Code: "malformed_response", Message: err.Error()})
		return err
	}
	// Actions run first. A failed/unknown external mutation is dead-lettered as
	// ambiguous and is never automatically replayed by the fabric.
	if err := a.applyActionsForPlugin(ctx, pluginID, response.Actions); err != nil {
		_ = a.events.Complete(context.Background(), work.eventID, work.deliveryID, work.revision, pluginv1.EventHandlerResult{Outcome: pluginv1.EventAmbiguous, Code: "action_outcome_unknown", Message: err.Error()})
		return err
	}
	for _, emitted := range response.Events {
		emitted.CausationID = work.eventID
		if emitted.TraceID == "" {
			emitted.TraceID = work.request.Event.Envelope.TraceID
		}
		if emitted.CorrelationID == "" {
			emitted.CorrelationID = work.request.Event.Envelope.CorrelationID
		}
		if _, err := a.events.PublishDerived(ctx, eventbus.Identity{PluginID: pluginID, AccountID: work.request.Event.Envelope.AccountID}, emitted, work.request.Event.Envelope); err != nil {
			_ = a.events.Complete(context.Background(), work.eventID, work.deliveryID, work.revision, pluginv1.EventHandlerResult{Outcome: pluginv1.EventRetryable, Code: "emission_failed", Message: err.Error()})
			return err
		}
	}
	result := pluginv1.EventHandlerResult{Outcome: pluginv1.EventAcknowledged}
	if response.EventResult != nil {
		result = *response.EventResult
	}
	if err := a.events.Complete(context.Background(), work.eventID, work.deliveryID, work.revision, result); err != nil {
		return fmt.Errorf("complete event delivery: %w", err)
	}
	return nil
}
