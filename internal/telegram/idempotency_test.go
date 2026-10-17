package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type countingTransport struct {
	mu    sync.Mutex
	count int
	err   error
}

func (t *countingTransport) Run(context.Context, Sink) error { return nil }
func (t *countingTransport) Apply(_ context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	t.mu.Lock()
	t.count++
	t.mu.Unlock()
	if t.err != nil {
		return pluginv1.ActionResult{}, t.err
	}
	return pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultSuccess, Success: true, MessageID: 7}, nil
}
func (*countingTransport) Close(context.Context) error { return nil }

func TestApplyBatchIdempotencyAndPartialStop(t *testing.T) {
	transport := &countingTransport{}
	store, err := NewIdempotencyStore(4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	action := pluginv1.Action{ID: "a", Kind: pluginv1.ActionSend, ChatID: 1, Text: "x", IdempotencyKey: "k"}
	first, err := ApplyBatchResults(context.Background(), transport, pluginv1.ActionBatchRequest{Actions: []pluginv1.Action{action}}, "p/account/1", store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyBatchResults(context.Background(), transport, pluginv1.ActionBatchRequest{Actions: []pluginv1.Action{{ID: "different", Kind: pluginv1.ActionSend, ChatID: 1, Text: "x", IdempotencyKey: "k"}}}, "p/account/1", store)
	if err != nil {
		t.Fatal(err)
	}
	if transport.count != 1 || first.Results[0].MessageID != second.Results[0].MessageID {
		t.Fatalf("count=%d first=%#v second=%#v", transport.count, first, second)
	}
	other, _ := ApplyBatchResults(context.Background(), transport, pluginv1.ActionBatchRequest{Actions: []pluginv1.Action{{ID: "other", Kind: pluginv1.ActionSend, ChatID: 1, Text: "x", IdempotencyKey: "k"}}}, "other/account/1", store)
	if transport.count != 2 || other.Results[0].Status != pluginv1.ActionResultSuccess {
		t.Fatalf("scope isolation: count=%d result=%#v", transport.count, other)
	}
}

func TestApplyBatchUnknownAndStopOnError(t *testing.T) {
	transport := &countingTransport{err: context.DeadlineExceeded}
	request := pluginv1.ActionBatchRequest{StopOnError: true, Actions: []pluginv1.Action{{ID: "a", Kind: pluginv1.ActionSend, ChatID: 1, Text: "x"}, {ID: "b", Kind: pluginv1.ActionSend, ChatID: 1, Text: "y"}}}
	response, err := ApplyBatchResults(context.Background(), transport, request, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || !response.Stopped || !response.Partial || !response.Results[0].OutcomeUnknown {
		t.Fatalf("response=%#v", response)
	}
	if !errors.Is(response.Results[0].Error, context.DeadlineExceeded) && response.Results[0].Error == nil {
		t.Fatal("unknown result missing error")
	}
}
