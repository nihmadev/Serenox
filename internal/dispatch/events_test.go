package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInlineRouterMatchesOncePerPlugin(t *testing.T) {
	router, err := NewInlineRouter(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []InlineTarget{{PluginID: "p", HandlerID: "a"}, {PluginID: "p", HandlerID: "b", QueryPrefix: "x"}} {
		if err := router.Register(target); err != nil {
			t.Fatal(err)
		}
	}
	var got []InlineTarget
	result := router.Route(context.Background(), InlineQuery{SenderID: 1, Query: "x"}, func(_ context.Context, target InlineTarget) error {
		got = append(got, target)
		return nil
	})
	if result.Matched != 2 || result.Enqueued != 1 || len(got) != 1 {
		t.Fatalf("route result=%+v targets=%+v", result, got)
	}
	if err := router.Register(InlineTarget{PluginID: "p", HandlerID: "a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate registration error=%v", err)
	}
}

func TestCallbackRouterExpiresDynamicEntries(t *testing.T) {
	router, err := NewCallbackRouter(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.RegisterDynamic("x", "p", "callback", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	result := router.Route(context.Background(), Callback{ID: "c", SenderID: 1, Data: "x"}, func(_ context.Context, _ CallbackTarget) error { return nil })
	if result.Matched != 0 {
		t.Fatalf("expired callback matched: %+v", result)
	}
}
