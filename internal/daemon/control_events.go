package daemon

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/nihmadev/Serenox/internal/eventbus"
)

type EventSummary struct {
	EventID        string                 `json:"event_id"`
	Type           string                 `json:"type"`
	Version        int                    `json:"version"`
	Source         string                 `json:"source"`
	AccountID      int64                  `json:"account_id"`
	AcceptedUnixMS int64                  `json:"accepted_unix_ms"`
	DeliveryClass  string                 `json:"delivery_class"`
	Deliveries     []EventDeliverySummary `json:"deliveries,omitempty"`
}
type EventDeliverySummary struct {
	ID            string `json:"id"`
	PluginID      string `json:"plugin_id"`
	HandlerID     string `json:"handler_id"`
	State         string `json:"state"`
	Attempt       int    `json:"attempt"`
	LastErrorCode string `json:"last_error_code,omitempty"`
}

func (s *ControlServer) handleEvents(connection net.Conn, request controlRequest) {
	ctx, cancel := context.WithTimeout(s.ctx, controlTimeout)
	defer cancel()
	switch request.Operation {
	case "events_status":
		s.handleEventStatus(ctx, connection)
	case "events_inspect":
		s.handleEventInspect(ctx, connection, request.EventID)
	case "events_list":
		s.handleEventList(ctx, connection, request.State, request.Limit)
	case "events_retry":
		if s.eventReplay == nil || request.EventID == "" || request.SubscriptionID == "" {
			s.writeResponse(connection, controlResponse{Error: "event_id and subscription_id are required"})
			return
		}
		if err := s.eventReplay(ctx, request.EventID, request.SubscriptionID); err != nil {
			s.writeResponse(connection, controlResponse{Error: "event retry failed"})
			return
		}
		s.writeResponse(connection, controlResponse{OK: true})
	case "events_discard":
		if !request.Confirmed {
			s.writeResponse(connection, controlResponse{Error: "explicit confirmation is required"})
			return
		}
		if s.eventDiscard == nil || request.EventID == "" {
			s.writeResponse(connection, controlResponse{Error: "event_id is required"})
			return
		}
		if err := s.eventDiscard(ctx, request.EventID); err != nil {
			s.writeResponse(connection, controlResponse{Error: "event discard failed"})
			return
		}
		s.writeResponse(connection, controlResponse{OK: true})
	default:
		s.writeResponse(connection, controlResponse{Error: "unknown event operation"})
	}
}

func (s *ControlServer) handleEventStatus(ctx context.Context, connection net.Conn) {
	if s.eventStatus == nil {
		s.writeResponse(connection, controlResponse{Error: "event operations are unavailable"})
		return
	}
	status, err := s.eventStatus(ctx)
	if err != nil {
		s.writeResponse(connection, controlResponse{Error: "event status failed"})
		return
	}
	s.writeResponse(connection, controlResponse{OK: true, EventStatus: &status})
}
func (s *ControlServer) handleEventInspect(ctx context.Context, connection net.Conn, eventID string) {
	if s.eventGet == nil {
		s.writeResponse(connection, controlResponse{Error: "event operations are unavailable"})
		return
	}
	record, found, err := s.eventGet(ctx, eventID)
	if err != nil {
		s.writeResponse(connection, controlResponse{Error: "event inspect failed"})
		return
	}
	if !found {
		s.writeResponse(connection, controlResponse{Error: "event was not found"})
		return
	}
	s.writeResponse(connection, controlResponse{OK: true, Events: []EventSummary{summarizeEvent(record)}})
}
func (s *ControlServer) handleEventList(ctx context.Context, connection net.Conn, state string, limit int) {
	if s.eventList == nil {
		s.writeResponse(connection, controlResponse{Error: "event operations are unavailable"})
		return
	}
	if limit == 0 {
		limit = 100
	}
	records, err := s.eventList(ctx, eventbus.DeliveryState(state), limit)
	if err != nil {
		s.writeResponse(connection, controlResponse{Error: "event list failed"})
		return
	}
	summaries := make([]EventSummary, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, summarizeEvent(record))
	}
	s.writeResponse(connection, controlResponse{OK: true, Events: summaries})
}

func summarizeEvent(record eventbus.Record) EventSummary {
	summary := EventSummary{EventID: record.Envelope.ID, Type: record.Envelope.Type, Version: record.Envelope.Version, Source: record.Envelope.Source, AccountID: record.Envelope.AccountID, AcceptedUnixMS: record.Envelope.AcceptedUnixMS, DeliveryClass: string(record.Class), Deliveries: make([]EventDeliverySummary, 0, len(record.Deliveries))}
	for _, delivery := range record.Deliveries {
		code := delivery.LastError
		if index := strings.IndexByte(code, ':'); index >= 0 {
			code = code[:index]
		}
		summary.Deliveries = append(summary.Deliveries, EventDeliverySummary{ID: delivery.ID, PluginID: delivery.PluginID, HandlerID: delivery.Subscription.HandlerID, State: string(delivery.State), Attempt: delivery.Attempt, LastErrorCode: code})
	}
	return summary
}

func EventRuntimeStatus(ctx context.Context, dataRoot string) (eventbus.Stats, error) {
	response, err := callControlResponse(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "events_status"})
	if err != nil {
		return eventbus.Stats{}, err
	}
	if response.EventStatus == nil {
		return eventbus.Stats{}, errors.New("daemon returned no event status")
	}
	return *response.EventStatus, nil
}
func EventRuntimeList(ctx context.Context, dataRoot, state string, limit int) ([]EventSummary, error) {
	response, err := callControlResponse(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "events_list", State: state, Limit: limit})
	return response.Events, err
}
func EventRuntimeInspect(ctx context.Context, dataRoot, eventID string) ([]EventSummary, error) {
	response, err := callControlResponse(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "events_inspect", EventID: eventID})
	return response.Events, err
}
func EventRuntimeRetry(ctx context.Context, dataRoot, eventID, subscriptionID string) error {
	return callControl(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "events_retry", EventID: eventID, SubscriptionID: subscriptionID})
}
func EventRuntimeDiscard(ctx context.Context, dataRoot, eventID string, confirmed bool) error {
	return callControl(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "events_discard", EventID: eventID, Confirmed: confirmed})
}
