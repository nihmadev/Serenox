// profile.clock is a bounded example userbot plugin. It owns only the
// first/last-name lease and uses the core durable Jobs API for minute ticks.
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

const stateKey = "profile"

type clockState struct {
	AccountID  int64  `json:"account_id"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name,omitempty"`
	Generation uint64 `json:"generation"`
	LeaseToken string `json:"lease_token"`
	JobID      string `json:"job_id"`
	Timezone   string `json:"timezone"`
	LastValue  string `json:"last_value,omitempty"`
	Uncertain  bool   `json:"uncertain,omitempty"`
}

type handler struct {
	mu       sync.Mutex
	timezone *time.Location
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serenox.Run(ctx, &handler{timezone: time.UTC}, serenox.Config{Workers: 1, QueueSize: 8, WriteTimeout: 2 * time.Second})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "profile.clock:", err)
		os.Exit(1)
	}
}

func (h *handler) Metadata(context.Context) (protocol.PluginMetadata, error) {
	return protocol.PluginMetadata{
		ID: "profile.clock", Name: "Profile Clock", Version: "1.0.0",
		Description: "Displays the original profile name followed by the local time.", Author: "Serenox", ProtocolVersion: protocol.Version,
		Capabilities: []protocol.Capability{protocol.CapabilityCommands, protocol.CapabilityStorage, protocol.CapabilityClientIdentity, protocol.CapabilityProfile, protocol.CapabilityJobs},
		Commands:     []protocol.CommandSpec{{Name: "clock", Description: "enable, disable, or inspect the profile clock", OwnerOnly: true}},
		Jobs:         []protocol.JobHandlerSpec{{Name: "tick", Description: "Update the profile clock once per minute", Persistent: true, MaxRuntimeMS: 30_000, Overlap: protocol.JobOverlapSkipIfRunning, RequiredCapabilities: []protocol.Capability{protocol.CapabilityProfile}, RequiredPermissions: []string{protocol.ProfilePermission}, PayloadType: "profile.clock/tick/v1", MaxPayloadBytes: 128}},
		Config:       []protocol.ConfigSpec{{Key: "timezone", Type: protocol.ConfigString, Description: "Explicit IANA timezone; UTC by default", Default: json.RawMessage(`"UTC"`)}},
	}, nil
}

func (h *handler) Configure(_ context.Context, values []protocol.ConfigValue) error {
	zone := "UTC"
	for _, value := range values {
		if value.Key == "timezone" {
			if err := json.Unmarshal(value.Value, &zone); err != nil {
				return fmt.Errorf("timezone must be a string")
			}
		}
	}
	zone = strings.TrimSpace(zone)
	if zone == "" || len(zone) > 64 || zone == "Local" {
		return errors.New("timezone must be a bounded IANA name")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return fmt.Errorf("load timezone: %w", err)
	}
	h.mu.Lock()
	h.timezone = location
	h.mu.Unlock()
	return nil
}

func (h *handler) Health(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error) {
	return protocol.HealthResponse{Ready: true, Status: "ready"}, nil
}

func (h *handler) Start(context.Context) error { return nil }
func (h *handler) Stop(context.Context) error  { return nil }

func (h *handler) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if request.Command == nil || request.Command.Name != "clock" {
		return protocol.DispatchResponse{}, nil
	}
	if len(request.Command.Args) == 0 {
		return h.reply(request.Command.Message, "Usage: /clock enable|disable|status")
	}
	services, ok := serenox.ServicesFromContext(ctx)
	if !ok || services.Actions == nil || services.Client == nil || services.Storage == nil || services.Jobs == nil {
		return protocol.DispatchResponse{}, errors.New("profile.clock host services are unavailable")
	}
	switch strings.ToLower(request.Command.Args[0]) {
	case "enable":
		return h.enable(ctx, services, request.Command.Message)
	case "disable":
		return h.disable(ctx, services, request.Command.Message)
	case "status":
		return h.status(ctx, services, request.Command.Message)
	default:
		return h.reply(request.Command.Message, "Usage: /clock enable|disable|status")
	}
}

func (h *handler) Job(ctx context.Context, invocation protocol.JobInvocation) (protocol.DispatchResponse, error) {
	services, ok := serenox.ServicesFromContext(ctx)
	if !ok || services.Actions == nil || services.Storage == nil {
		return protocol.DispatchResponse{}, errors.New("profile.clock job services are unavailable")
	}
	state, found, err := h.load(ctx, services.Storage)
	if err != nil || !found || state.Uncertain {
		return protocol.DispatchResponse{}, err
	}
	if state.JobID != invocation.Identity.ID {
		return protocol.DispatchResponse{}, nil
	}
	return protocol.DispatchResponse{}, h.tick(ctx, services, &state)
}

func (h *handler) enable(ctx context.Context, services serenox.Services, message protocol.Message) (protocol.DispatchResponse, error) {
	identity, err := services.Client.Identity.GetMe(ctx)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if identity == nil || identity.FirstName == "" {
		return protocol.DispatchResponse{}, errors.New("typed account identity has no first name")
	}
	state, found, err := h.load(ctx, services.Storage)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if found && state.Generation != 0 && !state.Uncertain {
		return h.reply(message, "Profile clock is already enabled.")
	}
	lease, err := services.Actions.ProfileAcquire(ctx, "profile-clock-acquire", "profile-clock-acquire-v1")
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if lease.Status != protocol.ActionResultSuccess || lease.Profile == nil {
		return protocol.DispatchResponse{}, actionResultError(lease)
	}
	state = clockState{AccountID: identity.ID, FirstName: identity.FirstName, LastName: identity.LastName, Generation: lease.Profile.Generation, LeaseToken: lease.Profile.LeaseToken, JobID: "profile-clock-minute", Timezone: h.location().String()}
	if lease.Profile.FirstName != state.FirstName || lease.Profile.LastName != state.LastName {
		return protocol.DispatchResponse{}, errors.New("profile lease identity changed during capture")
	}
	spec := protocol.JobSpec{Identity: protocol.JobIdentity{ID: state.JobID}, Handler: "tick", Description: "Profile clock minute update", Schedule: protocol.JobSchedule{Kind: protocol.JobScheduleFixedRate, Interval: &protocol.IntervalSchedule{IntervalMS: 60_000}}, Timezone: protocol.JobTimezone{Name: state.Timezone, DST: protocol.JobDSTFirstFold}, Persistent: true, DeadlineMS: 55_000, MaxRuntimeMS: 30_000, Misfire: protocol.JobMisfireSkip, Overlap: protocol.JobOverlapSkipIfRunning, PayloadType: "profile.clock/tick/v1"}
	if _, err := services.Jobs.Create(ctx, spec); err != nil {
		_, _ = services.Actions.ProfileRestore(ctx, "profile-clock-restore", "profile-clock-restore-v1", state.Generation, state.LeaseToken)
		_, _ = services.Actions.ProfileRelease(ctx, "profile-clock-release", "profile-clock-release-v1", state.Generation, state.LeaseToken)
		return protocol.DispatchResponse{}, err
	}
	if err := h.save(ctx, services.Storage, state); err != nil {
		return protocol.DispatchResponse{}, err
	}
	if err := h.tick(ctx, services, &state); err != nil {
		return protocol.DispatchResponse{}, err
	}
	return h.reply(message, "Profile clock enabled (UTC is the default timezone).")
}

func (h *handler) disable(ctx context.Context, services serenox.Services, message protocol.Message) (protocol.DispatchResponse, error) {
	state, found, err := h.load(ctx, services.Storage)
	if err != nil || !found {
		return h.reply(message, "Profile clock is already disabled.")
	}
	if err := services.Jobs.Delete(ctx, protocol.JobIdentityRequest{Identity: protocol.JobIdentity{ID: state.JobID}}); err != nil {
		return protocol.DispatchResponse{}, err
	}
	restore, err := services.Actions.ProfileRestore(ctx, "profile-clock-restore", "profile-clock-restore-v1", state.Generation, state.LeaseToken)
	if err != nil || restore.Status == protocol.ActionResultOutcomeUnknown {
		return protocol.DispatchResponse{}, actionResultError(restore)
	}
	release, err := services.Actions.ProfileRelease(ctx, "profile-clock-release", "profile-clock-release-v1", state.Generation, state.LeaseToken)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if release.Status != protocol.ActionResultSuccess {
		return protocol.DispatchResponse{}, actionResultError(release)
	}
	return h.reply(message, "Profile clock disabled and the original profile name was restored.")
}

func (h *handler) status(ctx context.Context, services serenox.Services, message protocol.Message) (protocol.DispatchResponse, error) {
	state, found, err := h.load(ctx, services.Storage)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if !found {
		return h.reply(message, "Profile clock is disabled.")
	}
	return h.reply(message, "Profile clock is enabled in "+state.Timezone+".")
}

func (h *handler) tick(ctx context.Context, services serenox.Services, state *clockState) error {
	zone := h.location()
	now := time.Now().In(zone)
	value := state.FirstName + " | " + now.Format("15:04")
	if len([]byte(value)) > 64 {
		return &protocol.Error{Code: protocol.ErrorProfileInvalidName, Message: "original first name is too long for the bounded clock format"}
	}
	if value == state.LastValue {
		return nil
	}
	result, err := services.Actions.ProfileUpdate(ctx, "profile-clock-update", "profile-clock-"+now.Format("200601021504"), value, state.LastName, state.Generation, state.LeaseToken)
	if err != nil {
		return err
	}
	if result.Status == protocol.ActionResultOutcomeUnknown {
		state.Uncertain = true
		_ = h.save(ctx, services.Storage, *state)
		return nil
	}
	if result.Status != protocol.ActionResultSuccess && result.Status != protocol.ActionResultSkipped {
		return actionResultError(result)
	}
	state.LastValue = value
	return h.save(ctx, services.Storage, *state)
}

func (h *handler) location() *time.Location {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timezone == nil {
		return time.UTC
	}
	return h.timezone
}
func (h *handler) load(ctx context.Context, storage *serenox.StorageClient) (clockState, bool, error) {
	raw, found, err := storage.Get(ctx, "profile.clock", stateKey)
	if err != nil || !found {
		return clockState{}, found, err
	}
	var state clockState
	err = json.Unmarshal(raw, &state)
	return state, true, err
}
func (h *handler) save(ctx context.Context, storage *serenox.StorageClient, state clockState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return storage.Set(ctx, "profile.clock", stateKey, raw)
}
func (h *handler) reply(message protocol.Message, text string) (protocol.DispatchResponse, error) {
	return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "profile-clock-reply", Kind: protocol.ActionReply, ChatID: message.ChatID, ReplyToMessageID: message.ID, Text: text}}}, nil
}
func actionResultError(result protocol.ActionResult) error {
	if result.Error != nil {
		return result.Error
	}
	return fmt.Errorf("profile action returned %s", result.Status)
}
