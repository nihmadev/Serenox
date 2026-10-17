package telegram

import (
	"context"
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestRecordedReadServiceUsesBoundedScopedPages(t *testing.T) {
	transport, err := ReadRecorded(&reader{data: []byte(`{"id":"r","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":42,"date_unix_ms":1,"text":"fixture"}}
`)}, RecordedOptions{ClientResults: map[pluginv1.ClientReadOperation]pluginv1.ClientResult{
		pluginv1.ClientGetHistory: {Messages: []pluginv1.Message{{ID: 3, ChatID: 42, DateUnixMS: 1}, {ID: 2, ChatID: 42, DateUnixMS: 1}, {ID: 1, ChatID: 42, DateUnixMS: 1}}, Page: &pluginv1.PageInfo{HasMore: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	scope := ReadScope{PluginID: "plugin.a", AccountID: 9}
	first, err := transport.Read(context.Background(), scope, pluginv1.ClientRequest{Operation: pluginv1.ClientGetHistory, PeerID: 42, Limit: 2, Direction: "older"})
	if err != nil || first.Page == nil || first.Page.Cursor == "" || len(first.Messages) != 2 {
		t.Fatalf("first page: %#v %v", first, err)
	}
	second, err := transport.Read(context.Background(), scope, pluginv1.ClientRequest{Operation: pluginv1.ClientGetHistory, PeerID: 42, Limit: 2, Cursor: first.Page.Cursor, Direction: "older"})
	if err != nil || len(second.Messages) != 1 {
		t.Fatalf("second page: %#v %v", second, err)
	}
	if _, err := transport.Read(context.Background(), ReadScope{PluginID: "plugin.b", AccountID: 9}, pluginv1.ClientRequest{Operation: pluginv1.ClientGetHistory, PeerID: 42, Limit: 2, Cursor: first.Page.Cursor, Direction: "older"}); err == nil {
		t.Fatal("cursor replay unexpectedly succeeded")
	}
}
