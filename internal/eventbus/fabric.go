package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Fabric struct {
	mu            sync.RWMutex
	store         Store
	clock         Clock
	ids           IDGenerator
	metrics       Metrics
	limits        Limits
	registrations map[string]Registration
	rates         map[string]rateWindow
}

type rateWindow struct {
	startedUnixMS int64
	events        int
	bytes         int64
}

func New(store Store, ids IDGenerator, clock Clock, metrics Metrics, limits Limits) (*Fabric, error) {
	if store == nil || ids == nil {
		return nil, fmt.Errorf("eventbus: store and ID generator are required")
	}
	if clock == nil {
		clock = realClock{}
	}
	if metrics == nil {
		metrics = noopMetrics{}
	}
	defaults := DefaultLimits()
	if limits.MaxRetainedEvents == 0 {
		limits = defaults
	}
	if limits.MaxRetainedEvents < 1 || limits.MaxRetainedBytes < 1024 || limits.MaxFanout < 1 || limits.MaxFanout > protocol.MaxEventFanout || limits.MaxHop < 1 || limits.MaxHop > protocol.MaxEventHop || limits.Retention <= 0 || limits.MaxList < 1 || limits.MaxTraceEvents < 1 || limits.MaxEventsPerMinute < 1 || limits.MaxBytesPerMinute < 1 || limits.MaxInFlightPerPublisher < 1 {
		return nil, fmt.Errorf("eventbus: invalid limits")
	}
	f := &Fabric{store: store, ids: ids, clock: clock, metrics: metrics, limits: limits, registrations: make(map[string]Registration), rates: make(map[string]rateWindow)}
	if _, err := f.Recover(context.Background()); err != nil {
		return nil, err
	}
	return f, nil
}

func registrationKey(pluginID string, accountID int64) string {
	return fmt.Sprintf("%s/%d", pluginID, accountID)
}
func declarationKey(eventType string, version int) string {
	return fmt.Sprintf("%s/v%d", eventType, version)
}

func (f *Fabric) Register(reg Registration) error {
	if reg.PluginID == "" || reg.AccountID == 0 {
		return fmt.Errorf("eventbus: plugin and account identity are required")
	}
	metadata := protocol.PluginMetadata{ID: reg.PluginID, Name: reg.PluginID, Version: "1", ProtocolVersion: protocol.Version, Capabilities: reg.Capabilities, EventPublications: reg.Publications, EventSubscriptions: reg.Subscriptions}
	if err := metadata.Validate(); err != nil {
		return err
	}
	permissionSet := make(map[string]struct{}, len(reg.Permissions))
	for _, permission := range reg.Permissions {
		permissionSet[permission] = struct{}{}
	}
	for _, declaration := range reg.Publications {
		if _, ok := permissionSet[protocol.EventPermission("publish", declaration.Type, declaration.Version)]; !ok {
			return fmt.Errorf("%w: %s", ErrPermission, declarationKey(declaration.Type, declaration.Version))
		}
	}
	for _, declaration := range reg.Subscriptions {
		if _, ok := permissionSet[protocol.EventPermission("subscribe", declaration.Type, declaration.Version)]; !ok {
			return fmt.Errorf("%w: %s", ErrPermission, declarationKey(declaration.Type, declaration.Version))
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registrations[registrationKey(reg.PluginID, reg.AccountID)] = cloneRegistration(reg)
	return nil
}

func (f *Fabric) Unregister(pluginID string, accountID int64) {
	f.mu.Lock()
	key := registrationKey(pluginID, accountID)
	delete(f.registrations, key)
	for rateKey := range f.rates {
		if rateKey == key || strings.HasPrefix(rateKey, key+"/") {
			delete(f.rates, rateKey)
		}
	}
	f.mu.Unlock()
}

func (f *Fabric) Get(ctx context.Context, eventID string) (Record, bool, error) {
	return f.store.Get(ctx, eventID)
}

func (f *Fabric) Publish(ctx context.Context, identity Identity, request protocol.EventPublishRequest) (protocol.EventPublishResult, error) {
	return f.publish(ctx, identity, request, 0)
}

func (f *Fabric) PublishDerived(ctx context.Context, identity Identity, request protocol.EventPublishRequest, parent protocol.EventEnvelope) (protocol.EventPublishResult, error) {
	request.CausationID = parent.ID
	if request.TraceID == "" {
		request.TraceID = parent.TraceID
	}
	if request.CorrelationID == "" {
		request.CorrelationID = parent.CorrelationID
	}
	return f.publish(ctx, identity, request, parent.Hop+1)
}

func (f *Fabric) publish(ctx context.Context, identity Identity, request protocol.EventPublishRequest, hop int) (protocol.EventPublishResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.EventPublishResult{}, err
	}
	if identity.PluginID == "" || identity.AccountID == 0 {
		return protocol.EventPublishResult{}, ErrPermission
	}
	if err := request.Validate(); err != nil {
		return protocol.EventPublishResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	registration, ok := f.registrations[registrationKey(identity.PluginID, identity.AccountID)]
	if !ok {
		return protocol.EventPublishResult{}, ErrPermission
	}
	publication, ok := declaredPublication(registration, request.Type, request.Version)
	if !ok {
		return protocol.EventPublishResult{}, ErrNotDeclared
	}
	if !slices.Contains(registration.Permissions, protocol.EventPermission("publish", request.Type, request.Version)) {
		return protocol.EventPublishResult{}, ErrPermission
	}
	if len(request.Payload) > publication.MaxPayloadBytes {
		return protocol.EventPublishResult{}, fmt.Errorf("%w: declared payload limit", protocol.ErrInvalidDTO)
	}
	records, err := f.store.List(ctx, f.limits.MaxRetainedEvents+1)
	if err != nil && err != ErrQuota {
		return protocol.EventPublishResult{}, err
	}
	if request.DedupeKey != "" {
		if result, found := findDedupe(records, identity, request, publication.DeliveryClass); found {
			f.metrics.Observe("deduplicated", request.Type, identity.PluginID, "accepted")
			return result, nil
		}
	}
	if len(records) >= f.limits.MaxRetainedEvents {
		return protocol.EventPublishResult{}, ErrQuota
	}
	now := f.clock.Now().UTC()
	rateKey := registrationKey(identity.PluginID, identity.AccountID)
	window := f.rates[rateKey]
	if window.startedUnixMS == 0 || now.UnixMilli()-window.startedUnixMS >= 60_000 {
		window = rateWindow{startedUnixMS: now.UnixMilli()}
	}
	if window.events >= f.limits.MaxEventsPerMinute || window.bytes+int64(len(request.Payload)) > f.limits.MaxBytesPerMinute {
		return protocol.EventPublishResult{}, ErrQuota
	}
	typeRateKey := rateKey + "/" + request.Type
	typeWindow := f.rates[typeRateKey]
	if typeWindow.startedUnixMS == 0 || now.UnixMilli()-typeWindow.startedUnixMS >= 60_000 {
		typeWindow = rateWindow{startedUnixMS: now.UnixMilli()}
	}
	if typeWindow.events >= f.limits.MaxEventsPerMinute || typeWindow.bytes+int64(len(request.Payload)) > f.limits.MaxBytesPerMinute {
		return protocol.EventPublishResult{}, ErrQuota
	}
	inFlight := 0
	for _, record := range records {
		if record.Envelope.Source == identity.PluginID && record.Envelope.AccountID == identity.AccountID && hasUnfinished(record) {
			inFlight++
		}
	}
	if inFlight >= f.limits.MaxInFlightPerPublisher {
		return protocol.EventPublishResult{}, ErrQuota
	}
	if hop > f.limits.MaxHop {
		return protocol.EventPublishResult{}, ErrHopLimit
	}
	if request.TraceID != "" {
		count := 0
		for _, record := range records {
			if record.Envelope.TraceID == request.TraceID {
				count++
			}
		}
		if count >= f.limits.MaxTraceEvents {
			return protocol.EventPublishResult{}, ErrQuota
		}
	}
	deliveries := f.route(identity, request, publication.DeliveryClass)
	if len(deliveries) > f.limits.MaxFanout {
		return protocol.EventPublishResult{}, ErrFanout
	}
	eventID := f.ids.NewEventID()
	if eventID == "" {
		return protocol.EventPublishResult{}, fmt.Errorf("eventbus: ID generator returned empty ID")
	}
	traceID := request.TraceID
	if traceID == "" {
		traceID = eventID
	}
	occurred := request.OccurredUnixMS
	if occurred == 0 {
		occurred = now.UnixMilli()
	}
	envelope := protocol.EventEnvelope{ID: eventID, Type: request.Type, Version: request.Version, Source: identity.PluginID, AccountID: identity.AccountID, OccurredUnixMS: occurred, AcceptedUnixMS: now.UnixMilli(), CorrelationID: request.CorrelationID, CausationID: request.CausationID, TraceID: traceID, DedupeKey: request.DedupeKey, ContentType: "application/json", Attempt: 1, Hop: hop, Payload: append(json.RawMessage(nil), request.Payload...)}
	record := Record{SchemaVersion: 1, Envelope: envelope, Class: publication.DeliveryClass, Deliveries: deliveries, ExpiresUnixMS: now.Add(f.limits.Retention).UnixMilli()}
	retainedBytes := int64(0)
	for _, existing := range records {
		encoded, _ := json.Marshal(existing)
		retainedBytes += int64(len(encoded))
	}
	encoded, _ := json.Marshal(record)
	if retainedBytes+int64(len(encoded)) > f.limits.MaxRetainedBytes {
		return protocol.EventPublishResult{}, ErrQuota
	}
	if err := f.store.Put(ctx, record, 0); err != nil {
		f.metrics.Observe("rejected", request.Type, identity.PluginID, "storage")
		return protocol.EventPublishResult{}, err
	}
	window.events++
	window.bytes += int64(len(request.Payload))
	f.rates[rateKey] = window
	typeWindow.events++
	typeWindow.bytes += int64(len(request.Payload))
	f.rates[typeRateKey] = typeWindow
	f.metrics.Observe("published", request.Type, identity.PluginID, "accepted")
	return protocol.EventPublishResult{EventID: eventID, Accepted: true, MatchedSubscribers: len(deliveries), DeliveryClass: publication.DeliveryClass}, nil
}

func (f *Fabric) route(identity Identity, request protocol.EventPublishRequest, class protocol.EventDeliveryClass) []Delivery {
	result := make([]Delivery, 0)
	for _, reg := range f.registrations {
		if reg.AccountID != identity.AccountID {
			continue
		}
		for _, sub := range reg.Subscriptions {
			if sub.Type != request.Type || sub.Version != request.Version || sub.DeliveryClass != class {
				continue
			}
			if sub.SourcePolicy == protocol.EventSourceAnyPublic && strings.HasPrefix(identity.PluginID, "core.") {
				continue
			}
			if sub.SourcePolicy == protocol.EventSourceExact && sub.SourcePlugin != identity.PluginID {
				continue
			}
			if sub.SourcePolicy == protocol.EventSourceCore && !strings.HasPrefix(identity.PluginID, "core.") {
				continue
			}
			id := reg.PluginID + "/" + sub.HandlerID
			result = append(result, Delivery{ID: id, PluginID: reg.PluginID, Subscription: sub, State: StatePending, Revision: 1})
		}
	}
	slices.SortFunc(result, func(a, b Delivery) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func declaredPublication(reg Registration, eventType string, version int) (protocol.EventPublication, bool) {
	for _, item := range reg.Publications {
		if item.Type == eventType && item.Version == version {
			return item, true
		}
	}
	return protocol.EventPublication{}, false
}
func findDedupe(records []Record, identity Identity, request protocol.EventPublishRequest, class protocol.EventDeliveryClass) (protocol.EventPublishResult, bool) {
	for _, record := range records {
		if record.Envelope.Source == identity.PluginID && record.Envelope.AccountID == identity.AccountID && record.Envelope.Type == request.Type && record.Envelope.Version == request.Version && record.Envelope.DedupeKey == request.DedupeKey {
			return protocol.EventPublishResult{EventID: record.Envelope.ID, Accepted: true, Deduplicated: true, MatchedSubscribers: len(record.Deliveries), DeliveryClass: class}, true
		}
	}
	return protocol.EventPublishResult{}, false
}
func cloneRegistration(reg Registration) Registration {
	reg.Capabilities = slices.Clone(reg.Capabilities)
	reg.Permissions = slices.Clone(reg.Permissions)
	reg.Publications = slices.Clone(reg.Publications)
	reg.Subscriptions = slices.Clone(reg.Subscriptions)
	return reg
}

func hasUnfinished(record Record) bool {
	for _, delivery := range record.Deliveries {
		if delivery.State != StateAcknowledged && delivery.State != StateDeadLettered {
			return true
		}
	}
	return false
}
