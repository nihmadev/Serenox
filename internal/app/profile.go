package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type profileLease struct {
	pluginID   string
	accountID  int64
	generation uint64
	token      string
	original   pluginv1.Peer
	current    pluginv1.Peer
}

type profileLeaseManager struct {
	mu      sync.Mutex
	lease   *profileLease
	nextGen uint64
	results map[string]pluginv1.ActionResult
}

func newProfileLeaseManager() *profileLeaseManager {
	return &profileLeaseManager{results: make(map[string]pluginv1.ActionResult)}
}

func newLeaseToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func profileFailure(action pluginv1.Action, err error) pluginv1.ActionResult {
	result := pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultFailure, Success: false}
	var protocolErr *pluginv1.Error
	if errors.As(err, &protocolErr) {
		result.Error = protocolErr
		if protocolErr.Code == pluginv1.ErrorOutcomeUnknown {
			result.Status, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, true
		}
		result.Retryable = protocolErr.Retryable
	} else {
		result.Error = &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "profile mutation outcome is unknown"}
		result.Status, result.OutcomeUnknown = pluginv1.ActionResultOutcomeUnknown, true
	}
	return result
}

func profileSuccess(action pluginv1.Action, peer pluginv1.Peer, lease *profileLease, released bool) pluginv1.ActionResult {
	result := pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultSuccess, Success: true, Profile: &pluginv1.ProfileResult{FirstName: peer.FirstName, LastName: peer.LastName, Released: released}}
	if lease != nil {
		result.Profile.Generation, result.Profile.LeaseToken = lease.generation, lease.token
	}
	return result
}

func (a *App) applyProfileAction(ctx context.Context, pluginID string, action pluginv1.Action) (result pluginv1.ActionResult, err error) {
	started := time.Now()
	defer func() {
		if a.metrics == nil {
			return
		}
		state := resultState(result)
		_ = a.metrics.Add("serenox_profile_operations_total", map[string]string{"operation": string(profileOperation(action)), "state": state}, 1)
		_ = a.metrics.Add("serenox_profile_latency_ms_total", nil, time.Since(started).Milliseconds())
		if result.Error != nil {
			switch result.Error.Code {
			case pluginv1.ErrorProfileLeaseConflict:
				_ = a.metrics.Add("serenox_profile_lease_conflicts_total", nil, 1)
			case pluginv1.ErrorOutcomeUnknown:
				_ = a.metrics.Add("serenox_profile_outcome_unknown_total", nil, 1)
			case pluginv1.ErrorFloodWait:
				_ = a.metrics.Add("serenox_profile_flood_wait_total", nil, 1)
			}
		}
		if result.Status == pluginv1.ActionResultSkipped {
			_ = a.metrics.Add("serenox_profile_skipped_equal_total", nil, 1)
		}
	}()
	if action.Profile == nil {
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "profile action is missing its typed payload"}), nil
	}
	if err := action.Profile.Validate(); err != nil {
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: err.Error()}), nil
	}
	key := pluginID + "\x00" + fmt.Sprint(a.SelfID()) + "\x00" + action.OperationName() + "\x00" + action.IdempotencyKey
	client, ok := a.transport.(telegram.ProfileClient)
	if !ok {
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "profile update is unavailable on this transport"}), nil
	}
	a.profile.mu.Lock()
	defer a.profile.mu.Unlock()
	if cached, ok := a.profile.results[key]; ok {
		return cached, nil
	}
	defer func() {
		if action.IdempotencyKey != "" && result.ActionID != "" {
			a.profile.results[key] = result
		}
	}()
	scope := a.SelfID()
	if action.Profile.Operation == pluginv1.ProfileAcquire {
		if a.profile.lease != nil {
			return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorProfileLeaseConflict, Message: "profile display-name lease is already owned"}), nil
		}
		identity, err := client.GetProfileIdentity(ctx)
		if err != nil {
			return profileFailure(action, err), nil
		}
		if identity == nil || identity.ID != scope || identity.FirstName == "" {
			return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorProfileInvalidName, Message: "current profile identity is incomplete"}), nil
		}
		token, err := newLeaseToken()
		if err != nil {
			return profileFailure(action, err), nil
		}
		a.profile.nextGen++
		lease := &profileLease{pluginID: pluginID, accountID: scope, generation: a.profile.nextGen, token: token, original: *identity, current: *identity}
		a.profile.lease = lease
		return profileSuccess(action, *identity, lease, false), nil
	}
	lease := a.profile.lease
	if lease == nil || lease.pluginID != pluginID || lease.accountID != scope || lease.generation != action.Profile.Generation || lease.token != action.Profile.LeaseToken {
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorProfileStaleLease, Message: "profile lease is stale or not owned by this plugin"}), nil
	}
	identity, err := client.GetProfileIdentity(ctx)
	if err != nil {
		return profileFailure(action, err), nil
	}
	if identity == nil || identity.FirstName != lease.current.FirstName || identity.LastName != lease.current.LastName {
		a.profile.lease = nil
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorProfileStaleLease, Message: "profile changed outside the active lease"}), nil
	}
	if action.Profile.Operation == pluginv1.ProfileRelease {
		a.profile.lease = nil
		return profileSuccess(action, *identity, lease, true), nil
	}
	target := pluginv1.Peer{ID: scope, Kind: "user", FirstName: action.Profile.FirstName, LastName: action.Profile.LastName}
	if action.Profile.Operation == pluginv1.ProfileRestore {
		target.FirstName, target.LastName = lease.original.FirstName, lease.original.LastName
	}
	if target.FirstName == lease.current.FirstName && target.LastName == lease.current.LastName {
		result := profileSuccess(action, lease.current, lease, false)
		result.Status, result.Success = pluginv1.ActionResultSkipped, false
		return result, nil
	}
	var updated *pluginv1.Peer
	if recorder, ok := client.(telegram.ProfileActionRecorder); ok {
		recordedAction := action
		if action.Profile.Operation == pluginv1.ProfileRestore {
			profile := *action.Profile
			profile.FirstName, profile.LastName = target.FirstName, target.LastName
			recordedAction.Profile = &profile
		}
		updated, err = recorder.ApplyProfileName(ctx, recordedAction)
	} else {
		updated, err = client.UpdateProfileName(ctx, target.FirstName, target.LastName)
	}
	if err != nil {
		return profileFailure(action, err), nil
	}
	if updated == nil || updated.FirstName != target.FirstName || updated.LastName != target.LastName {
		return profileFailure(action, &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "Telegram did not prove the requested profile fields"}), nil
	}
	lease.current = *updated
	return profileSuccess(action, *updated, lease, false), nil
}

func profileOperation(a pluginv1.Action) pluginv1.ProfileOperation {
	if a.Profile == nil {
		return "unknown"
	}
	return a.Profile.Operation
}

func (a *App) releaseProfileOwner(ctx context.Context, pluginID string) error {
	client, ok := a.transport.(telegram.ProfileClient)
	if !ok {
		return nil
	}
	a.profile.mu.Lock()
	defer a.profile.mu.Unlock()
	lease := a.profile.lease
	if lease == nil || lease.pluginID != pluginID {
		return nil
	}
	identity, err := client.GetProfileIdentity(ctx)
	if err == nil && identity != nil && identity.FirstName == lease.current.FirstName && identity.LastName == lease.current.LastName {
		updated, updateErr := client.UpdateProfileName(ctx, lease.original.FirstName, lease.original.LastName)
		if updateErr != nil {
			err = updateErr
		} else if updated == nil || updated.FirstName != lease.original.FirstName || updated.LastName != lease.original.LastName {
			err = fmt.Errorf("profile restore was not confirmed")
		}
	}
	a.profile.lease = nil
	for key := range a.profile.results {
		if len(key) >= len(pluginID) && key[:len(pluginID)] == pluginID {
			delete(a.profile.results, key)
		}
	}
	if err != nil && a.metrics != nil {
		_ = a.metrics.Add("serenox_profile_cleanup_failures_total", nil, 1)
	}
	return err
}

func (a *App) restoreAllProfiles(ctx context.Context) error {
	a.profile.mu.Lock()
	pluginID := ""
	if a.profile.lease != nil {
		pluginID = a.profile.lease.pluginID
	}
	a.profile.mu.Unlock()
	if pluginID == "" {
		return nil
	}
	return a.releaseProfileOwner(ctx, pluginID)
}
