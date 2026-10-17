package telegram

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestRecordedTransportFixtureAndActions(t *testing.T) {
	transport, err := LoadRecorded("testdata/updates.ndjson", RecordedOptions{MaxUpdates: 2, MaxActions: 1})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	err = transport.Run(context.Background(), SinkFunc(func(_ context.Context, update pluginv1.Update) error {
		ids = append(ids, update.ID)
		return nil
	}))
	if err != nil || len(ids) != 2 || ids[0] != "recorded-1" {
		t.Fatalf("unexpected replay: ids=%v err=%v", ids, err)
	}
	action := pluginv1.Action{ID: "a1", Kind: pluginv1.ActionReply, ChatID: 42, MessageID: 101, Text: "pong"}
	result, err := transport.Apply(context.Background(), action)
	if err != nil || !result.Success || result.ActionID != "a1" {
		t.Fatalf("unexpected action result: %#v %v", result, err)
	}
	if _, err := transport.Apply(context.Background(), pluginv1.Action{ID: "a2", Kind: pluginv1.ActionDelete, ChatID: 42, MessageID: 101}); !errors.Is(err, ErrActionLimit) {
		t.Fatalf("expected action cap, got %v", err)
	}
}

func TestRecordedRejectsMalformedAndBounded(t *testing.T) {
	if _, err := ReadRecorded(io.NopCloser(&reader{data: []byte("not-json\n")}), RecordedOptions{}); err == nil {
		t.Fatal("expected malformed fixture error")
	}
	valid := `{"id":"x","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":1,"date_unix_ms":1,"text":"x"}}` + "\n"
	if _, err := ReadRecorded(&reader{data: []byte(valid + valid)}, RecordedOptions{MaxUpdates: 1}); err == nil {
		t.Fatal("expected update cap")
	}
}

type reader struct{ data []byte }

func (r *reader) Read(payload []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(payload, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestFileSessionAtomicPermissionsAndCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "session.bin")
	session, err := NewFileSession(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Save([]byte("session")); err != nil {
		t.Fatal(err)
	}
	payload, err := session.Load()
	if err != nil || string(payload) != "session" {
		t.Fatalf("load: %q %v", payload, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	if err := session.Save([]byte("too large")); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("expected cap, got %v", err)
	}
	if err := os.WriteFile(path, []byte("too large"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Load(); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("expected bounded load, got %v", err)
	}
	if _, err := NewFileSession(path, MaximumSessionBytes+1); err == nil {
		t.Fatal("expected hard session cap validation")
	}
	if err := session.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted session stat error = %v", err)
	}
	if err := session.Delete(); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func validUpdate(id string) pluginv1.Update {
	return pluginv1.Update{ID: id, Kind: pluginv1.UpdateMessage, OccurredUnixMS: 1, Message: &pluginv1.Message{ID: 1, ChatID: 1, DateUnixMS: 1, Text: "x"}}
}

func TestIngressReservedLaneAndBackpressure(t *testing.T) {
	ingress, err := NewIngress(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ingress.TryUpdate(validUpdate("ordinary")); err != nil {
		t.Fatal(err)
	}
	if err := ingress.TryUpdate(validUpdate("dropped")); !errors.Is(err, ErrIngressFull) {
		t.Fatalf("expected full, got %v", err)
	}
	if err := ingress.SubmitCommand(context.Background(), validUpdate("command")); err != nil {
		t.Fatal(err)
	}
	first, err := ingress.Receive(context.Background())
	if err != nil || first.ID != "command" {
		t.Fatalf("reserved lane not prioritized: %#v %v", first, err)
	}
	ingress.Close()
}

type reconnectClient struct {
	mu       sync.Mutex
	connects int
	updates  []pluginv1.Update
}

func (c *reconnectClient) Connect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connects++
	if c.connects == 1 {
		return errors.New("temporary disconnect")
	}
	return nil
}
func (c *reconnectClient) NextUpdate(ctx context.Context) (pluginv1.Update, error) {
	c.mu.Lock()
	if len(c.updates) > 0 {
		update := c.updates[0]
		c.updates = c.updates[1:]
		c.mu.Unlock()
		return update, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return pluginv1.Update{}, ctx.Err()
}
func (c *reconnectClient) Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error) {
	return pluginv1.ActionResult{}, nil
}
func (c *reconnectClient) Close(context.Context) error { return nil }

func TestClientTransportReconnectAndCancellation(t *testing.T) {
	client := &reconnectClient{updates: []pluginv1.Update{validUpdate("connected")}}
	transport, err := NewClientTransport(client, ReconnectPolicy{Attempts: 2, Initial: time.Millisecond, Maximum: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan struct{})
	err = transport.Run(ctx, SinkFunc(func(context.Context, pluginv1.Update) error {
		close(seen)
		cancel()
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	client.mu.Lock()
	connects := client.connects
	client.mu.Unlock()
	if connects != 2 {
		t.Fatalf("expected reconnect, got %d connects", connects)
	}
}
