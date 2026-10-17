// profile.nowplaying shows the track playing on the local desktop in the
// account's last name. It watches MPRIS players over the D-Bus session bus and
// writes the profile only when the track or play state changes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

const (
	defaultFormat         = "🎧 {artist} — {title}"
	defaultFormatNoArtist = "🎧 {title}"
	defaultDebounce       = 1500 * time.Millisecond
	maxReconnectDelay     = 30 * time.Second
)

type settings struct {
	format       formatter
	filter       playerFilter
	clearOnPause bool
	debounce     time.Duration
	busAddress   string
}

type handler struct {
	ctrl *controller

	mu       sync.Mutex
	settings settings
	retry    *time.Timer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := newHandler()
	err := serenox.Run(ctx, h, serenox.Config{Workers: 1, QueueSize: 8, WriteTimeout: 2 * time.Second})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "profile.nowplaying:", err)
		os.Exit(1)
	}
}

func newHandler() *handler {
	defaults := settings{
		format:   formatter{withArtist: defaultFormat, withoutArtist: defaultFormatNoArtist},
		filter:   playerFilter{ignore: []string{"TelegramDesktop"}},
		debounce: defaultDebounce,
	}
	return &handler{ctrl: newController(defaults.format, false), settings: defaults}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "profile.nowplaying: "+format+"\n", args...)
}

func (h *handler) Metadata(context.Context) (protocol.PluginMetadata, error) {
	return protocol.PluginMetadata{
		ID: pluginID, Name: "Now Playing", Version: "1.0.1",
		Description: "Shows the track playing on this desktop (MPRIS over D-Bus) in the profile last name.", Author: "Serenox", ProtocolVersion: protocol.Version,
		Capabilities: []protocol.Capability{protocol.CapabilityCommands, protocol.CapabilityStorage, protocol.CapabilityProfile},
		Commands:     []protocol.CommandSpec{{Name: "np", Description: "enable, disable, or inspect the now-playing name", OwnerOnly: true}},
		Config: []protocol.ConfigSpec{
			{Key: "format", Type: protocol.ConfigString, Description: "Last-name template; placeholders {artist}, {title}, {album}", Default: json.RawMessage(`"` + defaultFormat + `"`)},
			{Key: "format_no_artist", Type: protocol.ConfigString, Description: "Template used when the player reports no artist", Default: json.RawMessage(`"` + defaultFormatNoArtist + `"`)},
			{Key: "players", Type: protocol.ConfigStrings, Description: "Only use players whose bus name or identity contains one of these; empty means all", Default: json.RawMessage(`[]`)},
			{Key: "ignore_players", Type: protocol.ConfigStrings, Description: "Never use players matching these", Default: json.RawMessage(`["TelegramDesktop"]`)},
			{Key: "clear_on_pause", Type: protocol.ConfigBoolean, Description: "Restore the original last name while playback is paused", Default: json.RawMessage(`false`)},
			{Key: "debounce_ms", Type: protocol.ConfigInteger, Description: "Coalesce bursts of player signals within this window", Default: json.RawMessage(`1500`)},
			{Key: "bus_address", Type: protocol.ConfigString, Description: "D-Bus session bus address; defaults to $XDG_RUNTIME_DIR/bus", Default: json.RawMessage(`""`)},
		},
	}, nil
}

func (h *handler) Configure(_ context.Context, values []protocol.ConfigValue) error {
	h.mu.Lock()
	next := h.settings
	h.mu.Unlock()
	for _, value := range values {
		var err error
		switch value.Key {
		case "format":
			err = decodeTemplate(value.Value, &next.format.withArtist)
		case "format_no_artist":
			err = decodeTemplate(value.Value, &next.format.withoutArtist)
		case "players":
			err = json.Unmarshal(value.Value, &next.filter.allow)
		case "ignore_players":
			err = json.Unmarshal(value.Value, &next.filter.ignore)
		case "clear_on_pause":
			err = json.Unmarshal(value.Value, &next.clearOnPause)
		case "debounce_ms":
			var ms int64
			if err = json.Unmarshal(value.Value, &ms); err == nil && (ms < 0 || ms > 60_000) {
				err = errors.New("must be within 0..60000")
			}
			next.debounce = time.Duration(ms) * time.Millisecond
		case "bus_address":
			err = json.Unmarshal(value.Value, &next.busAddress)
		}
		if err != nil {
			return fmt.Errorf("config %s: %w", value.Key, err)
		}
	}
	h.mu.Lock()
	h.settings = next
	h.mu.Unlock()
	h.ctrl.setFormat(next.format, next.clearOnPause)
	return nil
}

func decodeTemplate(raw json.RawMessage, target *string) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if !strings.Contains(value, "{title}") {
		return errors.New("must contain {title}")
	}
	*target = value
	return nil
}

func (h *handler) Health(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error) {
	return protocol.HealthResponse{Ready: true, Status: "ready"}, nil
}

func (h *handler) Start(context.Context) error { return nil }

func (h *handler) Stop(context.Context) error {
	// The core restores the original name itself when it unloads the plugin;
	// host RPC is not available while lifecycle.stop is being served.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.retry != nil {
		h.retry.Stop()
	}
	return nil
}

// Background watches the session bus for the lifetime of the connection and
// reconnects with backoff if the bus goes away (e.g. the user logs out).
func (h *handler) Background(ctx context.Context) error {
	services, ok := serenox.ServicesFromContext(ctx)
	if !ok || services.Actions == nil || services.Storage == nil {
		return errors.New("profile.nowplaying host services are unavailable")
	}
	h.ctrl.bind(services.Actions, services.Storage)
	if err := h.ctrl.resume(ctx); err != nil {
		logf("resume: %v", err)
	}
	h.mu.Lock()
	cfg := h.settings
	h.mu.Unlock()
	delay := time.Second
	for {
		watcher := &mprisWatcher{address: sessionBusAddress(cfg.busAddress), filter: cfg.filter, debounce: cfg.debounce}
		connected := time.Now()
		err := watcher.run(ctx, func(ctx context.Context, track *Track) {
			h.scheduleRetry(ctx, h.ctrl.observe(ctx, track))
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(connected) > time.Minute {
			delay = time.Second
		}
		logf("%v; reconnecting in %s", err, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

// scheduleRetry re-applies the latest track after a Telegram flood wait.
func (h *handler) scheduleRetry(ctx context.Context, wait time.Duration) {
	if wait <= 0 {
		return
	}
	logf("Telegram asked to wait %s before the next name change", wait)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.retry != nil {
		h.retry.Stop()
	}
	h.retry = time.AfterFunc(wait, func() {
		if ctx.Err() == nil {
			h.scheduleRetry(ctx, h.ctrl.retry(ctx))
		}
	})
}

func (h *handler) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if request.Command == nil || request.Command.Name != "np" {
		return protocol.DispatchResponse{}, nil
	}
	message := request.Command.Message
	services, ok := serenox.ServicesFromContext(ctx)
	if !ok || services.Actions == nil || services.Storage == nil {
		return protocol.DispatchResponse{}, errors.New("profile.nowplaying host services are unavailable")
	}
	h.ctrl.bind(services.Actions, services.Storage)
	command := "status"
	if len(request.Command.Args) > 0 {
		command = strings.ToLower(request.Command.Args[0])
	}
	var text string
	var err error
	switch command {
	case "enable", "on":
		text, err = h.ctrl.enable(ctx)
	case "disable", "off":
		text, err = h.ctrl.disable(ctx)
	case "status":
		text = h.ctrl.status()
	default:
		text = "Usage: /np enable|disable|status"
	}
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "nowplaying-reply", Kind: protocol.ActionReply, ChatID: message.ChatID, ReplyToMessageID: message.ID, Text: text}}}, nil
}
