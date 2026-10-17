// Package inline manages bounded, transport-neutral inline forms, lists, and
// galleries. Telegram Bot API implementations satisfy Backend; plugin callback
// functions never cross the process boundary, only opaque action IDs do.
package inline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrCapacity     = errors.New("inline unit capacity reached")
	ErrNotFound     = errors.New("inline unit not found")
	ErrUnauthorized = errors.New("inline callback is not authorized")
	ErrInvalid      = errors.New("invalid inline unit")
)

type Kind string

const (
	KindForm    Kind = "form"
	KindList    Kind = "list"
	KindGallery Kind = "gallery"
)

type Button struct {
	Text             string `json:"text"`
	URL              string `json:"url,omitempty"`
	ActionID         string `json:"action_id,omitempty"`
	Data             string `json:"data,omitempty"`
	InputPlaceholder string `json:"input_placeholder,omitempty"`
}

type Media struct {
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

type Unit struct {
	ID           string     `json:"id"`
	PluginID     string     `json:"plugin_id"`
	OwnerUserID  int64      `json:"owner_user_id"`
	AllowedUsers []int64    `json:"allowed_users,omitempty"`
	Kind         Kind       `json:"kind"`
	Text         string     `json:"text,omitempty"`
	Pages        []string   `json:"pages,omitempty"`
	Media        []Media    `json:"media,omitempty"`
	Buttons      [][]Button `json:"buttons,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
}

type CreateRequest struct {
	PluginID     string
	OwnerUserID  int64
	AllowedUsers []int64
	Kind         Kind
	Text         string
	Pages        []string
	Media        []Media
	Buttons      [][]Button
	TTL          time.Duration
}

type Callback struct {
	UnitID   string `json:"unit_id"`
	PluginID string `json:"plugin_id"`
	ActionID string `json:"action_id"`
	Data     string `json:"data,omitempty"`
	UserID   int64  `json:"user_id"`
	Input    string `json:"input,omitempty"`
}

type Limits struct {
	Units        int
	AllowedUsers int
	Rows         int
	Buttons      int
	Pages        int
	Media        int
	TextBytes    int
	PayloadBytes int
	DefaultTTL   time.Duration
	MaxTTL       time.Duration
	SweepEvery   time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		Units: 256, AllowedUsers: 32, Rows: 16, Buttons: 64,
		Pages: 50, Media: 128, TextBytes: 64 << 10, PayloadBytes: 4 << 10,
		DefaultTTL: 10 * time.Minute, MaxTTL: 24 * time.Hour, SweepEvery: time.Second,
	}
}

type Backend interface {
	Create(context.Context, Unit) error
	Edit(context.Context, Unit) error
	Delete(context.Context, string) error
}

type Manager struct {
	limits  Limits
	backend Backend
	mu      sync.RWMutex
	units   map[string]Unit
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewManager(limits Limits, backend Backend) (*Manager, error) {
	if limits.Units <= 0 || limits.AllowedUsers < 0 || limits.Rows <= 0 || limits.Buttons <= 0 ||
		limits.Pages <= 0 || limits.Media <= 0 || limits.TextBytes <= 0 || limits.PayloadBytes <= 0 ||
		limits.DefaultTTL <= 0 || limits.MaxTTL < limits.DefaultTTL || limits.SweepEvery <= 0 {
		return nil, errors.New("invalid inline limits")
	}
	return &Manager{limits: limits, backend: backend, units: make(map[string]Unit, limits.Units)}, nil
}

func (m *Manager) Start(parent context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return errors.New("inline manager already started")
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.run(ctx, m.done)
	return nil
}

func (m *Manager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(m.limits.SweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.expire(now)
		}
	}
}

func (m *Manager) Create(ctx context.Context, request CreateRequest) (Unit, error) {
	unit, err := m.build(request)
	if err != nil {
		return Unit{}, err
	}
	m.mu.Lock()
	if len(m.units) >= m.limits.Units {
		m.mu.Unlock()
		return Unit{}, ErrCapacity
	}
	m.units[unit.ID] = unit
	m.mu.Unlock()
	if m.backend != nil {
		if err := m.backend.Create(ctx, cloneUnit(unit)); err != nil {
			m.mu.Lock()
			delete(m.units, unit.ID)
			m.mu.Unlock()
			return Unit{}, fmt.Errorf("create inline unit: %w", err)
		}
	}
	return cloneUnit(unit), nil
}

func (m *Manager) Edit(ctx context.Context, id string, text string, buttons [][]Button) (Unit, error) {
	m.mu.Lock()
	unit, ok := m.units[id]
	if !ok || !unit.ExpiresAt.After(time.Now()) {
		delete(m.units, id)
		m.mu.Unlock()
		return Unit{}, ErrNotFound
	}
	if err := m.validateTextButtons(text, buttons); err != nil {
		m.mu.Unlock()
		return Unit{}, err
	}
	unit.Text = text
	unit.Buttons = cloneButtons(buttons)
	m.units[id] = unit
	m.mu.Unlock()
	if m.backend != nil {
		if err := m.backend.Edit(ctx, cloneUnit(unit)); err != nil {
			return Unit{}, fmt.Errorf("edit inline unit: %w", err)
		}
	}
	return cloneUnit(unit), nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	if _, ok := m.units[id]; !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	delete(m.units, id)
	m.mu.Unlock()
	if m.backend != nil {
		if err := m.backend.Delete(ctx, id); err != nil {
			return fmt.Errorf("delete inline unit: %w", err)
		}
	}
	return nil
}

func (m *Manager) HandleCallback(unitID, actionID string, userID int64, input string) (Callback, error) {
	m.mu.RLock()
	unit, ok := m.units[unitID]
	m.mu.RUnlock()
	if !ok || !unit.ExpiresAt.After(time.Now()) {
		return Callback{}, ErrNotFound
	}
	if !authorized(unit, userID) {
		return Callback{}, ErrUnauthorized
	}
	for _, row := range unit.Buttons {
		for _, button := range row {
			if button.ActionID == actionID && actionID != "" {
				return Callback{
					UnitID: unit.ID, PluginID: unit.PluginID, ActionID: actionID,
					Data: button.Data, UserID: userID, Input: input,
				}, nil
			}
		}
	}
	return Callback{}, ErrNotFound
}

func (m *Manager) Get(id string) (Unit, bool) {
	m.mu.RLock()
	unit, ok := m.units[id]
	m.mu.RUnlock()
	if !ok || !unit.ExpiresAt.After(time.Now()) {
		return Unit{}, false
	}
	return cloneUnit(unit), true
}

func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.units)
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	m.units = make(map[string]Unit, m.limits.Units)
	m.mu.Unlock()
	return nil
}

func (m *Manager) expire(now time.Time) {
	m.mu.Lock()
	for id, unit := range m.units {
		if !unit.ExpiresAt.After(now) {
			delete(m.units, id)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) build(request CreateRequest) (Unit, error) {
	if request.PluginID == "" || len(request.PluginID) > 128 || request.OwnerUserID == 0 ||
		(request.Kind != KindForm && request.Kind != KindList && request.Kind != KindGallery) ||
		len(request.AllowedUsers) > m.limits.AllowedUsers || len(request.Pages) > m.limits.Pages ||
		len(request.Media) > m.limits.Media {
		return Unit{}, ErrInvalid
	}
	if request.Kind == KindList && (len(request.Pages) == 0 || len(request.Pages) > 50) {
		return Unit{}, ErrInvalid
	}
	if request.Kind == KindGallery && len(request.Media) == 0 {
		return Unit{}, ErrInvalid
	}
	if err := m.validateTextButtons(request.Text, request.Buttons); err != nil {
		return Unit{}, err
	}
	for _, page := range request.Pages {
		if len(page) > m.limits.TextBytes {
			return Unit{}, ErrInvalid
		}
	}
	for _, media := range request.Media {
		if media.URL == "" || len(media.URL) > m.limits.PayloadBytes || len(media.Caption) > m.limits.TextBytes {
			return Unit{}, ErrInvalid
		}
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = m.limits.DefaultTTL
	}
	if ttl <= 0 || ttl > m.limits.MaxTTL {
		return Unit{}, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return Unit{}, fmt.Errorf("generate inline ID: %w", err)
	}
	now := time.Now().UTC()
	return Unit{
		ID: id, PluginID: request.PluginID, OwnerUserID: request.OwnerUserID,
		AllowedUsers: append([]int64(nil), request.AllowedUsers...), Kind: request.Kind,
		Text: request.Text, Pages: append([]string(nil), request.Pages...),
		Media: append([]Media(nil), request.Media...), Buttons: cloneButtons(request.Buttons),
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}, nil
}

func (m *Manager) validateTextButtons(text string, buttons [][]Button) error {
	if len(text) > m.limits.TextBytes || len(buttons) > m.limits.Rows {
		return ErrInvalid
	}
	count := 0
	for _, row := range buttons {
		if len(row) == 0 {
			return ErrInvalid
		}
		count += len(row)
		for _, button := range row {
			if button.Text == "" || len(button.Text) > 256 || len(button.Data) > m.limits.PayloadBytes ||
				(button.URL == "" && button.ActionID == "") || (button.URL != "" && button.ActionID != "") {
				return ErrInvalid
			}
		}
	}
	if count > m.limits.Buttons {
		return ErrInvalid
	}
	return nil
}

func authorized(unit Unit, userID int64) bool {
	if userID == unit.OwnerUserID {
		return true
	}
	for _, allowed := range unit.AllowedUsers {
		if allowed == userID {
			return true
		}
	}
	return false
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func cloneUnit(unit Unit) Unit {
	unit.AllowedUsers = append([]int64(nil), unit.AllowedUsers...)
	unit.Pages = append([]string(nil), unit.Pages...)
	unit.Media = append([]Media(nil), unit.Media...)
	unit.Buttons = cloneButtons(unit.Buttons)
	return unit
}

func cloneButtons(buttons [][]Button) [][]Button {
	result := make([][]Button, len(buttons))
	for index := range buttons {
		result[index] = append([]Button(nil), buttons[index]...)
	}
	return result
}
