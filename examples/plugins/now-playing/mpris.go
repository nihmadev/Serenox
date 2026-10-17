package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mprisPrefix      = "org.mpris.MediaPlayer2."
	mprisPath        = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	mprisRoot        = "org.mpris.MediaPlayer2"
	mprisPlayer      = "org.mpris.MediaPlayer2.Player"
	propertiesIface  = "org.freedesktop.DBus.Properties"
	propertiesSignal = propertiesIface + ".PropertiesChanged"
	ownerSignal      = "org.freedesktop.DBus.NameOwnerChanged"
	busCallTimeout   = 2 * time.Second
)

const (
	statusPlaying = "Playing"
	statusPaused  = "Paused"
	statusStopped = "Stopped"
)

// Track is one MPRIS player's state. MPRIS has no dedicated "track ended"
// signal: a new track arrives as a Metadata change, the end of a queue as
// PlaybackStatus=Stopped, and a closed player as its bus name losing its owner.
type Track struct {
	Bus      string
	Identity string
	Status   string
	TrackID  string
	Title    string
	Artists  []string
	Album    string
}

// Key identifies the displayed track; position, volume and rate changes leave
// it unchanged.
func (t Track) Key() string {
	return t.Bus + "\x00" + t.TrackID + "\x00" + t.Title + "\x00" + strings.Join(t.Artists, "\x00")
}

type playerFilter struct {
	allow  []string
	ignore []string
}

// accepts matches case-insensitive substrings against the bus-name suffix
// (e.g. "spotify", "chromium.instance42") and the player's Identity.
func (f playerFilter) accepts(bus, identity string) bool {
	name := strings.ToLower(strings.TrimPrefix(bus, mprisPrefix))
	identity = strings.ToLower(identity)
	matches := func(patterns []string) bool {
		for _, pattern := range patterns {
			pattern = strings.ToLower(strings.TrimSpace(pattern))
			if pattern != "" && (strings.Contains(name, pattern) || strings.Contains(identity, pattern)) {
				return true
			}
		}
		return false
	}
	if matches(f.ignore) {
		return false
	}
	return len(f.allow) == 0 || matches(f.allow)
}

// sessionBusAddress resolves the user session bus. The core starts plugins
// with a minimal environment, so DBUS_SESSION_BUS_ADDRESS is normally absent
// and the systemd default socket under the runtime directory is used.
func sessionBusAddress(configured string) string {
	if configured != "" {
		return configured
	}
	if value := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); value != "" {
		return value
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return "unix:path=" + filepath.Join(runtimeDir, "bus")
}

type mprisWatcher struct {
	address  string
	filter   playerFilter
	debounce time.Duration
	selected string
}

// run connects to the session bus and calls deliver with the preferred
// player's track (nil when nothing is playing) after every relevant change.
// Bursts of signals are coalesced by the debounce window. It returns when ctx
// ends or the bus connection is lost.
func (w *mprisWatcher) run(ctx context.Context, deliver func(context.Context, *Track)) error {
	conn, err := dbus.Connect(w.address, dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("connect session bus %s: %w", w.address, err)
	}
	defer conn.Close() //nolint:errcheck
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchObjectPath(mprisPath), dbus.WithMatchInterface(propertiesIface),
		dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchArg(0, mprisPlayer),
	); err != nil {
		return fmt.Errorf("subscribe to MPRIS properties: %w", err)
	}
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg0Namespace(mprisRoot),
	); err != nil {
		return fmt.Errorf("subscribe to MPRIS players: %w", err)
	}
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal, ok := <-signals:
			if !ok {
				return errors.New("session bus connection closed")
			}
			if relevantSignal(signal) {
				resetTimer(timer, w.debounce)
			}
		case <-timer.C:
			track, err := w.snapshot(ctx, conn)
			if err != nil {
				return err
			}
			deliver(ctx, track)
		}
	}
}

func relevantSignal(signal *dbus.Signal) bool {
	switch signal.Name {
	case ownerSignal:
		return true
	case propertiesSignal:
		if len(signal.Body) < 3 {
			return false
		}
		if changed, ok := signal.Body[1].(map[string]dbus.Variant); ok {
			if _, found := changed["Metadata"]; found {
				return true
			}
			if _, found := changed["PlaybackStatus"]; found {
				return true
			}
		}
		if invalidated, ok := signal.Body[2].([]string); ok {
			for _, name := range invalidated {
				if name == "Metadata" || name == "PlaybackStatus" {
					return true
				}
			}
		}
	}
	return false
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

func (w *mprisWatcher) snapshot(ctx context.Context, conn *dbus.Conn) (*Track, error) {
	callCtx, cancel := context.WithTimeout(ctx, busCallTimeout)
	defer cancel()
	var names []string
	if err := conn.BusObject().CallWithContext(callCtx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil, fmt.Errorf("list bus names: %w", err)
	}
	sort.Strings(names)
	var tracks []Track
	for _, bus := range names {
		if !strings.HasPrefix(bus, mprisPrefix) {
			continue
		}
		track, ok := readPlayer(ctx, conn, bus)
		if ok && w.filter.accepts(bus, track.Identity) {
			tracks = append(tracks, track)
		}
	}
	chosen := choosePlayer(tracks, w.selected)
	w.selected = ""
	if chosen != nil {
		w.selected = chosen.Bus
	}
	return chosen, nil
}

// readPlayer reports false for players that vanish or misbehave mid-query;
// they are skipped rather than failing the whole snapshot.
func readPlayer(ctx context.Context, conn *dbus.Conn, bus string) (Track, bool) {
	callCtx, cancel := context.WithTimeout(ctx, busCallTimeout)
	defer cancel()
	object := conn.Object(bus, mprisPath)
	var props map[string]dbus.Variant
	if err := object.CallWithContext(callCtx, propertiesIface+".GetAll", 0, mprisPlayer).Store(&props); err != nil {
		return Track{}, false
	}
	track := parseTrack(bus, props)
	var identity dbus.Variant
	if err := object.CallWithContext(callCtx, propertiesIface+".Get", 0, mprisRoot, "Identity").Store(&identity); err == nil {
		track.Identity, _ = identity.Value().(string)
	}
	return track, true
}

func parseTrack(bus string, props map[string]dbus.Variant) Track {
	track := Track{Bus: bus}
	if status, ok := props["PlaybackStatus"]; ok {
		track.Status, _ = status.Value().(string)
	}
	raw, ok := props["Metadata"]
	if !ok {
		return track
	}
	metadata, ok := raw.Value().(map[string]dbus.Variant)
	if !ok {
		return track
	}
	switch id := variantValue(metadata, "mpris:trackid").(type) {
	case dbus.ObjectPath:
		track.TrackID = string(id)
	case string:
		track.TrackID = id
	}
	track.Title, _ = variantValue(metadata, "xesam:title").(string)
	track.Album, _ = variantValue(metadata, "xesam:album").(string)
	switch artists := variantValue(metadata, "xesam:artist").(type) {
	case []string:
		for _, artist := range artists {
			if artist = strings.TrimSpace(artist); artist != "" {
				track.Artists = append(track.Artists, artist)
			}
		}
	case string:
		if artist := strings.TrimSpace(artists); artist != "" {
			track.Artists = []string{artist}
		}
	}
	return track
}

func variantValue(values map[string]dbus.Variant, key string) any {
	if value, ok := values[key]; ok {
		return value.Value()
	}
	return nil
}

// choosePlayer prefers a playing player over a paused one and keeps the
// previously selected player while it stays in the same tier, so two open
// players do not make the name flip back and forth.
func choosePlayer(tracks []Track, previous string) *Track {
	for _, status := range []string{statusPlaying, statusPaused} {
		var first *Track
		for index := range tracks {
			track := &tracks[index]
			if track.Status != status || strings.TrimSpace(track.Title) == "" {
				continue
			}
			if track.Bus == previous {
				return track
			}
			if first == nil {
				first = track
			}
		}
		if first != nil {
			return first
		}
	}
	return nil
}
