package main

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// privateBus starts an isolated dbus-daemon so the test never touches the
// user's real players.
func privateBus(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	cmd := exec.Command(binary, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(address)
}

type fakePlayer struct {
	conn  *dbus.Conn
	props *prop.Properties
}

func startFakePlayer(t *testing.T, address, name string, metadata map[string]dbus.Variant) *fakePlayer {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	props, err := prop.Export(conn, mprisPath, prop.Map{
		mprisRoot:   {"Identity": {Value: "Fake Player", Emit: prop.EmitFalse}},
		mprisPlayer: {"PlaybackStatus": {Value: statusPlaying, Emit: prop.EmitTrue}, "Metadata": {Value: metadata, Emit: prop.EmitTrue}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(mprisPrefix+name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	return &fakePlayer{conn: conn, props: props}
}

func song(id, title, artist string) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/track/" + id)),
		"xesam:title":   dbus.MakeVariant(title),
		"xesam:artist":  dbus.MakeVariant([]string{artist}),
	}
}

func TestWatcherFollowsSignalsOnPrivateBus(t *testing.T) {
	address := privateBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshots := make(chan *Track, 16)
	watcher := &mprisWatcher{address: address, debounce: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		done <- watcher.run(ctx, func(_ context.Context, track *Track) { snapshots <- track })
	}()
	next := func(want string) {
		t.Helper()
		for {
			select {
			case track := <-snapshots:
				got := ""
				if track != nil {
					got = track.Status + ":" + track.Title
				}
				if got == want {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q", want)
			}
		}
	}
	next("") // no players yet

	player := startFakePlayer(t, address, "fake", song("1", "First", "Band"))
	next(statusPlaying + ":First") // NameOwnerChanged

	player.props.SetMust(mprisPlayer, "Metadata", song("2", "Second", "Band"))
	next(statusPlaying + ":Second") // PropertiesChanged(Metadata) = track change

	player.props.SetMust(mprisPlayer, "PlaybackStatus", statusStopped)
	next("") // end of queue

	player.props.SetMust(mprisPlayer, "PlaybackStatus", statusPlaying)
	next(statusPlaying + ":Second")

	_ = player.conn.Close()
	next("") // player closed

	cancel()
	<-done
}
