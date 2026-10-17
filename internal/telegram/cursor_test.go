package telegram

import (
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestCursorCodecBindsScopeAndExpires(t *testing.T) {
	codec, err := NewCursorCodec([]byte("01234567890123456789012345678901"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scope := CursorScope{PluginID: "plugin.a", AccountID: 7, Operation: string(pluginv1.ClientGetHistory), PeerID: 42, Direction: "older", Query: "x"}
	cursor, err := codec.Encode(scope, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(cursor) > pluginv1.MaxClientCursorBytes {
		t.Fatal("cursor exceeded protocol limit")
	}
	position, err := codec.Decode(scope, cursor)
	if err != nil || position != 101 {
		t.Fatalf("decode: %d %v", position, err)
	}
	if _, err := codec.Decode(CursorScope{PluginID: "plugin.b", AccountID: 7, Operation: string(pluginv1.ClientGetHistory), PeerID: 42, Direction: "older", Query: "x"}, cursor); !errors.Is(err, ErrCursorScope) {
		t.Fatalf("replay error = %v", err)
	}
	if _, err := codec.Decode(CursorScope{PluginID: "plugin.a", AccountID: 7, Operation: string(pluginv1.ClientSearchMessages), PeerID: 42, Direction: "older", Query: "x"}, cursor); !errors.Is(err, ErrCursorScope) {
		t.Fatalf("operation mismatch = %v", err)
	}
}
