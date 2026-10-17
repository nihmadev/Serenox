package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// fakeCore mirrors the core's display-name lease rules in internal/app/profile.go.
type fakeCore struct {
	first, last string
	lease       *fakeLease
	nextGen     uint64
	writes      []string
	floodOnce   int64
}

type fakeLease struct {
	generation                uint64
	token                     string
	origFirst, origLast       string
	currentFirst, currentLast string
}

func failure(code protocol.ErrorCode) protocol.ActionResult {
	return protocol.ActionResult{Status: protocol.ActionResultFailure, Error: &protocol.Error{Code: code, Message: string(code)}}
}

func (f *fakeCore) owned(generation uint64, token string) (*fakeLease, *protocol.ActionResult) {
	if f.lease == nil || f.lease.generation != generation || f.lease.token != token {
		result := failure(protocol.ErrorProfileStaleLease)
		return nil, &result
	}
	if f.first != f.lease.currentFirst || f.last != f.lease.currentLast {
		f.lease = nil
		result := failure(protocol.ErrorProfileStaleLease)
		return nil, &result
	}
	return f.lease, nil
}

func (f *fakeCore) ProfileAcquire(context.Context, string, string) (protocol.ActionResult, error) {
	if f.lease != nil {
		return failure(protocol.ErrorProfileLeaseConflict), nil
	}
	f.nextGen++
	f.lease = &fakeLease{generation: f.nextGen, token: "t" + string(rune('0'+f.nextGen)), origFirst: f.first, origLast: f.last, currentFirst: f.first, currentLast: f.last}
	return protocol.ActionResult{Status: protocol.ActionResultSuccess, Success: true, Profile: &protocol.ProfileResult{FirstName: f.first, LastName: f.last, Generation: f.lease.generation, LeaseToken: f.lease.token}}, nil
}

func (f *fakeCore) set(lease *fakeLease, first, last string) protocol.ActionResult {
	if f.floodOnce > 0 {
		wait := f.floodOnce
		f.floodOnce = 0
		return protocol.ActionResult{Status: protocol.ActionResultFailure, Error: &protocol.Error{Code: protocol.ErrorFloodWait, Retryable: true, FloodWaitSeconds: wait}}
	}
	if first == lease.currentFirst && last == lease.currentLast {
		return protocol.ActionResult{Status: protocol.ActionResultSkipped}
	}
	f.first, f.last = first, last
	lease.currentFirst, lease.currentLast = first, last
	f.writes = append(f.writes, last)
	return protocol.ActionResult{Status: protocol.ActionResultSuccess, Success: true, Profile: &protocol.ProfileResult{FirstName: first, LastName: last, Generation: lease.generation, LeaseToken: lease.token}}
}

func (f *fakeCore) ProfileUpdate(_ context.Context, _, _ string, first, last string, generation uint64, token string) (protocol.ActionResult, error) {
	lease, stale := f.owned(generation, token)
	if stale != nil {
		return *stale, nil
	}
	return f.set(lease, first, last), nil
}

func (f *fakeCore) ProfileRestore(_ context.Context, _, _ string, generation uint64, token string) (protocol.ActionResult, error) {
	lease, stale := f.owned(generation, token)
	if stale != nil {
		return *stale, nil
	}
	return f.set(lease, lease.origFirst, lease.origLast), nil
}

func (f *fakeCore) ProfileRelease(_ context.Context, _, _ string, generation uint64, token string) (protocol.ActionResult, error) {
	if _, stale := f.owned(generation, token); stale != nil {
		return *stale, nil
	}
	f.lease = nil
	return protocol.ActionResult{Status: protocol.ActionResultSuccess, Success: true, Profile: &protocol.ProfileResult{FirstName: f.first, LastName: f.last, Released: true}}, nil
}

type memoryStore map[string]json.RawMessage

func (m memoryStore) Get(_ context.Context, namespace, key string) (json.RawMessage, bool, error) {
	value, ok := m[namespace+"/"+key]
	return value, ok, nil
}

func (m memoryStore) Set(_ context.Context, namespace, key string, value json.RawMessage) error {
	m[namespace+"/"+key] = value
	return nil
}

func newTestController(core *fakeCore, store memoryStore) *controller {
	c := newController(formatter{withArtist: defaultFormat, withoutArtist: defaultFormatNoArtist}, false)
	c.bind(core, store)
	return c
}

func playing(title string, artists ...string) *Track {
	return &Track{Bus: mprisPrefix + "spotify", Status: statusPlaying, TrackID: "/t/" + title, Title: title, Artists: artists}
}

func TestControllerWritesOnlyOnTrackChange(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{first: "Ivan", last: "Petrov"}
	c := newTestController(core, memoryStore{})
	if err := c.resume(ctx); err != nil {
		t.Fatal(err)
	}
	c.observe(ctx, playing("Song A", "Band"))
	if _, err := c.enable(ctx); err != nil {
		t.Fatal(err)
	}
	c.observe(ctx, playing("Song A", "Band")) // seek / volume change: same track
	c.observe(ctx, &Track{Bus: mprisPrefix + "spotify", Status: statusPaused, TrackID: "/t/Song A", Title: "Song A", Artists: []string{"Band"}})
	c.observe(ctx, playing("Song B"))
	c.observe(ctx, &Track{Bus: mprisPrefix + "spotify", Status: statusStopped})
	want := []string{"🎧 Band — Song A", "🎧 Song B", "Petrov"}
	if strings.Join(core.writes, "|") != strings.Join(want, "|") {
		t.Fatalf("writes = %q, want %q", core.writes, want)
	}
	if _, err := c.disable(ctx); err != nil {
		t.Fatal(err)
	}
	if core.first != "Ivan" || core.last != "Petrov" || core.lease != nil {
		t.Fatalf("after disable: %q %q lease=%v", core.first, core.last, core.lease)
	}
}

func TestControllerClearsOnPauseWhenConfigured(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{first: "Ivan"}
	c := newTestController(core, memoryStore{})
	c.setFormat(c.format, true)
	_ = c.resume(ctx)
	c.observe(ctx, playing("Song"))
	_, _ = c.enable(ctx)
	c.observe(ctx, &Track{Bus: mprisPrefix + "spotify", Status: statusPaused, Title: "Song"})
	if core.last != "" || len(core.writes) != 2 {
		t.Fatalf("writes = %q, last = %q", core.writes, core.last)
	}
}

func TestControllerAdoptsManualRename(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{first: "Ivan", last: "Petrov"}
	c := newTestController(core, memoryStore{})
	_ = c.resume(ctx)
	c.observe(ctx, playing("Song A"))
	_, _ = c.enable(ctx)
	core.first, core.last = "Vanya", "Sidorov" // edited by hand in the Telegram app
	c.observe(ctx, playing("Song B"))
	if core.first != "Vanya" || core.last != "🎧 Song B" {
		t.Fatalf("profile = %q %q", core.first, core.last)
	}
	if c.state.FirstName != "Vanya" || c.state.LastName != "Sidorov" {
		t.Fatalf("original = %q %q", c.state.FirstName, c.state.LastName)
	}
}

func TestControllerRecoversOriginalAfterCrash(t *testing.T) {
	ctx := context.Background()
	store := memoryStore{}
	core := &fakeCore{first: "Ivan", last: "Petrov"}
	first := newTestController(core, store)
	_ = first.resume(ctx)
	first.observe(ctx, playing("Song A"))
	_, _ = first.enable(ctx)
	// The daemon dies without restoring: the lease is gone, the title stays.
	core.lease = nil

	second := newTestController(core, store)
	if err := second.resume(ctx); err != nil {
		t.Fatal(err)
	}
	if core.lease == nil || core.lease.origLast != "Petrov" {
		t.Fatalf("core captured original %+v, want last name Petrov", core.lease)
	}
	second.observe(ctx, playing("Song B"))
	if core.last != "🎧 Song B" {
		t.Fatalf("last = %q", core.last)
	}
	_, _ = second.disable(ctx)
	if core.last != "Petrov" {
		t.Fatalf("restored last = %q", core.last)
	}
}

func TestControllerHonoursFloodWait(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{first: "Ivan"}
	c := newTestController(core, memoryStore{})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	_ = c.resume(ctx)
	_, _ = c.enable(ctx)
	core.floodOnce = 30
	if wait := c.observe(ctx, playing("Song")); wait != 30*time.Second {
		t.Fatalf("wait = %s", wait)
	}
	if wait := c.observe(ctx, playing("Song 2")); wait <= 0 || len(core.writes) != 0 {
		t.Fatalf("wrote during flood wait: wait=%s writes=%q", wait, core.writes)
	}
	now = now.Add(31 * time.Second)
	if wait := c.retry(ctx); wait != 0 || core.last != "🎧 Song 2" {
		t.Fatalf("after wait: wait=%s last=%q", wait, core.last)
	}
}

func TestRenderTruncatesOnRuneBoundary(t *testing.T) {
	f := formatter{withArtist: defaultFormat, withoutArtist: defaultFormatNoArtist}
	value := f.render(Track{Title: "Очень длинное название трека, которое не влезет", Artists: []string{"Исполнитель"}})
	if len(value) > maxNameBytes || !utf8.ValidString(value) || !strings.HasSuffix(value, ellipsis) {
		t.Fatalf("render = %q (%d bytes)", value, len(value))
	}
	if got := f.render(Track{Title: " Song\n\tName ", Artists: []string{"A", "B"}}); got != "🎧 A, B — Song Name" {
		t.Fatalf("render = %q", got)
	}
	if got := f.render(Track{Artists: []string{"A"}}); got != "" {
		t.Fatalf("render without title = %q", got)
	}
}

func TestChoosePlayerPrefersPlayingAndStaysSticky(t *testing.T) {
	tracks := []Track{
		{Bus: "a", Status: statusPaused, Title: "x"},
		{Bus: "b", Status: statusPlaying, Title: "y"},
		{Bus: "c", Status: statusPlaying, Title: "z"},
		{Bus: "d", Status: statusPlaying},
	}
	if got := choosePlayer(tracks, ""); got == nil || got.Bus != "b" {
		t.Fatalf("chose %+v, want b", got)
	}
	if got := choosePlayer(tracks, "c"); got == nil || got.Bus != "c" {
		t.Fatalf("chose %+v, want sticky c", got)
	}
	if got := choosePlayer(tracks[:1], "b"); got == nil || got.Bus != "a" {
		t.Fatalf("chose %+v, want paused a", got)
	}
	if got := choosePlayer([]Track{{Bus: "a", Status: statusStopped, Title: "x"}}, ""); got != nil {
		t.Fatalf("chose stopped player %+v", got)
	}
}

func TestPlayerFilter(t *testing.T) {
	f := playerFilter{ignore: []string{"TelegramDesktop"}}
	if f.accepts(mprisPrefix+"TelegramDesktop", "Telegram") || !f.accepts(mprisPrefix+"spotify", "Spotify") {
		t.Fatal("ignore list not applied")
	}
	f.allow = []string{"yandex"}
	if !f.accepts(mprisPrefix+"chromium.instance7", "YandexMusic") || f.accepts(mprisPrefix+"spotify", "Spotify") {
		t.Fatal("allow list must match identity")
	}
}

func TestParseTrack(t *testing.T) {
	props := map[string]dbus.Variant{
		"PlaybackStatus": dbus.MakeVariant(statusPlaying),
		"Metadata": dbus.MakeVariant(map[string]dbus.Variant{
			"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/track/1")),
			"xesam:title":   dbus.MakeVariant("Macarena"),
			"xesam:artist":  dbus.MakeVariant([]string{"Pantheon", " "}),
			"xesam:album":   dbus.MakeVariant("Single"),
		}),
	}
	track := parseTrack(mprisPrefix+"spotify", props)
	if track.Status != statusPlaying || track.TrackID != "/org/track/1" || track.Title != "Macarena" || len(track.Artists) != 1 || track.Album != "Single" {
		t.Fatalf("track = %+v", track)
	}
}

func TestRelevantSignalIgnoresPosition(t *testing.T) {
	position := &dbus.Signal{Name: propertiesSignal, Body: []any{mprisPlayer, map[string]dbus.Variant{"Position": dbus.MakeVariant(int64(5))}, []string{}}}
	metadata := &dbus.Signal{Name: propertiesSignal, Body: []any{mprisPlayer, map[string]dbus.Variant{"Metadata": dbus.MakeVariant(map[string]dbus.Variant{})}, []string{}}}
	if relevantSignal(position) || !relevantSignal(metadata) || !relevantSignal(&dbus.Signal{Name: ownerSignal}) {
		t.Fatal("signal relevance is wrong")
	}
}

func TestMetadataAndDefaultConfig(t *testing.T) {
	h := newHandler()
	metadata, err := h.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	values := make([]protocol.ConfigValue, 0, len(metadata.Config))
	for _, spec := range metadata.Config {
		values = append(values, protocol.ConfigValue{Key: spec.Key, Value: spec.Default})
	}
	if err := h.Configure(context.Background(), values); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	if h.settings.format.withArtist != defaultFormat || h.settings.debounce != defaultDebounce || len(h.settings.filter.ignore) != 1 {
		t.Fatalf("settings = %+v", h.settings)
	}
	if err := h.Configure(context.Background(), []protocol.ConfigValue{{Key: "format", Value: json.RawMessage(`"{artist}"`)}}); err == nil {
		t.Fatal("format without {title} was accepted")
	}
}

// The core rejects a whole dispatch response if one action lacks its
// permission, so the manifest must grant everything the plugin emits.
func TestManifestGrantsEmittedActions(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		ID          string   `json:"id"`
		Version     string   `json:"version"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	metadata, _ := newHandler().Metadata(context.Background())
	if manifest.ID != metadata.ID || manifest.Version != metadata.Version {
		t.Fatalf("manifest %s@%s, metadata %s@%s", manifest.ID, manifest.Version, metadata.ID, metadata.Version)
	}
	for _, want := range []string{"telegram.reply", protocol.ProfilePermission, "storage.read", "storage.write"} {
		found := false
		for _, permission := range manifest.Permissions {
			found = found || permission == want
		}
		if !found {
			t.Errorf("manifest is missing permission %q", want)
		}
	}
}
