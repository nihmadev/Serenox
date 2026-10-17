package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestLiveSessionBus reads the real desktop players. Run it the way the core
// starts plugins, with almost no environment, to exercise address discovery:
//
//	env -i PATH="$PATH" HOME="$HOME" NOWPLAYING_LIVE=1 go test -run Live -v ./examples/plugins/now-playing/
func TestLiveSessionBus(t *testing.T) {
	if os.Getenv("NOWPLAYING_LIVE") != "1" {
		t.Skip("set NOWPLAYING_LIVE=1 to read the local session bus")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	watcher := &mprisWatcher{address: sessionBusAddress(""), filter: playerFilter{ignore: []string{"TelegramDesktop"}}, debounce: 100 * time.Millisecond}
	got := make(chan *Track, 1)
	err := watcher.run(ctx, func(_ context.Context, track *Track) {
		select {
		case got <- track:
		default:
		}
		cancel()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("watch %s: %v", watcher.address, err)
	}
	track := <-got
	if track == nil {
		t.Log("no active player")
		return
	}
	f := formatter{withArtist: defaultFormat, withoutArtist: defaultFormatNoArtist}
	t.Logf("%s [%s] %q -> %q (%d bytes)", playerName(*track), track.Status, track.Title, f.render(*track), len(f.render(*track)))
}
