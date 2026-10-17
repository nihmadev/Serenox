package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	pluginID     = "profile.nowplaying"
	stateKey     = "state"
	maxFloodWait = time.Hour
)

// profileActions is the subset of the SDK action client the controller uses;
// tests substitute an in-memory core.
type profileActions interface {
	ProfileAcquire(ctx context.Context, id, idempotencyKey string) (protocol.ActionResult, error)
	ProfileUpdate(ctx context.Context, id, idempotencyKey, firstName, lastName string, generation uint64, leaseToken string) (protocol.ActionResult, error)
	ProfileRestore(ctx context.Context, id, idempotencyKey string, generation uint64, leaseToken string) (protocol.ActionResult, error)
	ProfileRelease(ctx context.Context, id, idempotencyKey string, generation uint64, leaseToken string) (protocol.ActionResult, error)
}

type stateStore interface {
	Get(ctx context.Context, namespace, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, namespace, key string, value json.RawMessage) error
}

// persisted survives plugin restarts. The original name is kept so a crash
// that leaves a track title in the profile does not become the new "original"
// on the next acquire.
type persisted struct {
	Enabled     bool   `json:"enabled"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	LeaseToken  string `json:"lease_token,omitempty"`
	LastWritten string `json:"last_written,omitempty"`
	Pending     string `json:"pending,omitempty"`
}

func (p persisted) leased() bool { return p.Generation != 0 && p.LeaseToken != "" }

// ours reports whether a last name found in the profile was written by this
// plugin rather than chosen by the user.
func (p persisted) ours(lastName string) bool {
	return lastName != "" && lastName != p.LastName && (lastName == p.LastWritten || lastName == p.Pending)
}

type controller struct {
	mu       sync.Mutex
	actions  profileActions
	store    stateStore
	format   formatter
	clearOn  bool // restore the original name while paused
	state    persisted
	loaded   bool
	track    *Track
	shown    string // last name currently in the profile, per the core
	retryAt  time.Time
	lastErr  string
	prefix   string
	sequence uint64
	now      func() time.Time
}

func newController(format formatter, clearOnPause bool) *controller {
	nonce := make([]byte, 6)
	_, _ = rand.Read(nonce)
	return &controller{format: format, clearOn: clearOnPause, prefix: "np-" + hex.EncodeToString(nonce), now: time.Now}
}

// bind attaches host clients. Background and command dispatch share one
// connection, so the first caller's clients serve both.
func (c *controller) bind(actions profileActions, store stateStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.actions == nil {
		c.actions, c.store = actions, store
	}
}

func (c *controller) setFormat(format formatter, clearOnPause bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.format, c.clearOn = format, clearOnPause
}

// key returns a process-unique idempotency key. The core caches results per
// key, so reusing one after a restart would replay a stale lease.
func (c *controller) key(operation string) string {
	c.sequence++
	return c.prefix + "-" + operation + "-" + strconv.FormatUint(c.sequence, 10)
}

func (c *controller) load(ctx context.Context) error {
	if c.loaded {
		return nil
	}
	raw, found, err := c.store.Get(ctx, pluginID, stateKey)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if found {
		if err := json.Unmarshal(raw, &c.state); err != nil {
			return fmt.Errorf("decode state: %w", err)
		}
	}
	c.loaded = true
	return nil
}

func (c *controller) save(ctx context.Context) error {
	raw, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	return c.store.Set(ctx, pluginID, stateKey, raw)
}

// resume re-acquires the profile after a plugin restart when it was enabled.
func (c *controller) resume(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(ctx); err != nil {
		return err
	}
	if !c.state.Enabled {
		return nil
	}
	if err := c.acquire(ctx); err != nil {
		return c.fail(err)
	}
	return c.fail(c.apply(ctx))
}

func (c *controller) enable(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(ctx); err != nil {
		return "", err
	}
	if c.state.Enabled && c.state.leased() && c.lastErr == "" {
		return "Now playing is already enabled.", nil
	}
	c.state.Enabled = true
	if err := c.acquire(ctx); err != nil {
		c.state.Enabled = false
		_ = c.save(ctx)
		return "", err
	}
	if err := c.apply(ctx); err != nil {
		return "Now playing enabled, but the first update failed: " + err.Error(), nil
	}
	return "Now playing enabled.", nil
}

func (c *controller) disable(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(ctx); err != nil {
		return "", err
	}
	if !c.state.Enabled && !c.state.leased() {
		return "Now playing is already disabled.", nil
	}
	var restoreErr error
	if c.state.leased() {
		restoreErr = c.restoreAndRelease(ctx)
	}
	c.state.Enabled, c.state.Generation, c.state.LeaseToken = false, 0, ""
	c.state.LastWritten, c.state.Pending, c.shown, c.lastErr = "", "", "", ""
	if err := c.save(ctx); err != nil {
		return "", err
	}
	if restoreErr != nil {
		return "Now playing disabled; restoring the name failed: " + restoreErr.Error(), nil
	}
	return "Now playing disabled and the original name was restored.", nil
}

func (c *controller) status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := "Now playing is disabled."
	if c.state.Enabled {
		text = "Now playing is enabled."
	}
	if c.track != nil {
		text += "\nPlayer: " + playerName(*c.track) + " (" + c.track.Status + ")\nTrack: " + c.format.render(*c.track)
	} else {
		text += "\nNo player is active."
	}
	if c.lastErr != "" {
		text += "\nLast error: " + c.lastErr
	}
	return text
}

// observe records the latest player snapshot and updates the profile when the
// displayed text changes. It returns the delay after which the caller should
// call retry, or zero.
func (c *controller) observe(ctx context.Context, track *Track) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.track = track
	return c.applyLocked(ctx)
}

func (c *controller) retry(ctx context.Context) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyLocked(ctx)
}

func (c *controller) applyLocked(ctx context.Context) time.Duration {
	if !c.loaded || !c.state.Enabled {
		return 0
	}
	if wait := c.retryAt.Sub(c.now()); wait > 0 {
		return wait
	}
	err := c.apply(ctx)
	_ = c.fail(err)
	var flood *protocol.Error
	if errors.As(err, &flood) && flood.Code == protocol.ErrorFloodWait {
		wait := time.Duration(flood.FloodWaitSeconds) * time.Second
		if wait <= 0 {
			wait = 5 * time.Second
		}
		wait = min(wait, maxFloodWait)
		c.retryAt = c.now().Add(wait)
		return wait
	}
	return 0
}

func (c *controller) desired() string {
	track := c.track
	if track == nil || track.Status == statusStopped || (track.Status == statusPaused && c.clearOn) {
		return c.state.LastName
	}
	if value := c.format.render(*track); value != "" {
		return value
	}
	return c.state.LastName
}

// apply writes the desired last name when it differs from what is shown, so
// the profile changes only when the track (or play state) changes.
func (c *controller) apply(ctx context.Context) error {
	if !c.state.Enabled {
		return nil
	}
	if !c.state.leased() {
		if err := c.acquire(ctx); err != nil {
			return err
		}
	}
	target := c.desired()
	if target == c.shown {
		return nil
	}
	err := c.update(ctx, target)
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) && protocolErr.Code == protocol.ErrorProfileStaleLease {
		// The name changed outside the lease (by hand, or an earlier uncertain
		// write landed). Re-acquire, adopting a manual rename as the new original.
		c.state.Generation, c.state.LeaseToken = 0, ""
		if err := c.acquire(ctx); err != nil {
			return err
		}
		if target = c.desired(); target == c.shown {
			return nil
		}
		err = c.update(ctx, target)
	}
	return err
}

func (c *controller) update(ctx context.Context, lastName string) error {
	result, err := c.actions.ProfileUpdate(ctx, "nowplaying-update", c.key("update"), c.state.FirstName, lastName, c.state.Generation, c.state.LeaseToken)
	if err != nil {
		return err
	}
	switch result.Status {
	case protocol.ActionResultSuccess, protocol.ActionResultSkipped:
		c.shown, c.retryAt = lastName, time.Time{}
		c.state.Pending = ""
		c.state.LastWritten = ""
		if lastName != c.state.LastName {
			c.state.LastWritten = lastName
		}
		return c.save(ctx)
	case protocol.ActionResultOutcomeUnknown:
		// Telegram may have applied it; remember the value so recovery can
		// tell it apart from a user's own rename.
		c.state.Pending = lastName
		_ = c.save(ctx)
	}
	return resultError(result)
}

// acquire takes the display-name lease. If a previous crash left one of our
// track titles in the profile, the stored original is written back first and
// the lease taken again, so the core captures the real original name.
func (c *controller) acquire(ctx context.Context) error {
	if c.state.leased() {
		// A lease from an earlier process of this plugin may still be live in
		// the core; hand it back so a fresh acquire does not conflict.
		_ = c.restoreAndRelease(ctx)
		c.state.Generation, c.state.LeaseToken = 0, ""
	}
	profile, err := c.acquireOnce(ctx)
	if err != nil {
		return err
	}
	if c.state.ours(profile.LastName) {
		generation, token := profile.Generation, profile.LeaseToken
		result, err := c.actions.ProfileUpdate(ctx, "nowplaying-recover", c.key("recover"), profile.FirstName, c.state.LastName, generation, token)
		if err == nil && result.Status != protocol.ActionResultSuccess && result.Status != protocol.ActionResultSkipped {
			err = resultError(result)
		}
		if err != nil {
			return fmt.Errorf("restore original last name after crash: %w", err)
		}
		if _, err := c.actions.ProfileRelease(ctx, "nowplaying-release", c.key("release"), generation, token); err != nil {
			return err
		}
		if profile, err = c.acquireOnce(ctx); err != nil {
			return err
		}
	}
	c.state.FirstName, c.state.LastName = profile.FirstName, profile.LastName
	c.state.Generation, c.state.LeaseToken = profile.Generation, profile.LeaseToken
	c.state.LastWritten, c.state.Pending = "", ""
	c.shown = profile.LastName
	return c.save(ctx)
}

func (c *controller) acquireOnce(ctx context.Context) (protocol.ProfileResult, error) {
	result, err := c.actions.ProfileAcquire(ctx, "nowplaying-acquire", c.key("acquire"))
	if err != nil {
		return protocol.ProfileResult{}, err
	}
	if result.Status != protocol.ActionResultSuccess || result.Profile == nil || result.Profile.LeaseToken == "" {
		return protocol.ProfileResult{}, resultError(result)
	}
	return *result.Profile, nil
}

func (c *controller) restoreAndRelease(ctx context.Context) error {
	generation, token := c.state.Generation, c.state.LeaseToken
	restore, err := c.actions.ProfileRestore(ctx, "nowplaying-restore", c.key("restore"), generation, token)
	if err == nil && restore.Status != protocol.ActionResultSuccess && restore.Status != protocol.ActionResultSkipped {
		err = resultError(restore)
	}
	release, releaseErr := c.actions.ProfileRelease(ctx, "nowplaying-release", c.key("release"), generation, token)
	if releaseErr == nil && release.Status != protocol.ActionResultSuccess {
		releaseErr = resultError(release)
	}
	return errors.Join(err, releaseErr)
}

func (c *controller) fail(err error) error {
	c.lastErr = ""
	if err != nil {
		c.lastErr = err.Error()
	}
	return err
}

func playerName(track Track) string {
	if track.Identity != "" {
		return track.Identity
	}
	return strings.TrimPrefix(track.Bus, mprisPrefix)
}

func resultError(result protocol.ActionResult) error {
	if result.Error != nil {
		return result.Error
	}
	return fmt.Errorf("profile action returned %s", result.Status)
}
