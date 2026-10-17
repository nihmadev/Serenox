package dispatch

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// InlineTarget and CallbackTarget are immutable registration records. Filters
// are deliberately small and contain no Telegram-library values.
type InlineTarget struct {
	PluginID     string
	HandlerID    string
	QueryPrefix  string
	SenderIDs    []int64
	AllowedUsers []int64
	OwnerOnly    bool
}

type CallbackTarget struct {
	PluginID     string
	HandlerID    string
	Data         string
	DataPrefix   string
	ChatIDs      []int64
	SenderIDs    []int64
	AllowedUsers []int64
	OwnerOnly    bool
}

type EventRouteResult struct{ Matched, Enqueued, Rejected int }

type InlineQuery struct {
	ID       string
	SenderID int64
	Query    string
}

type Callback struct {
	ID       string
	SenderID int64
	ChatID   int64
	Data     string
}

type InlineRouter struct {
	mu      sync.Mutex
	max     int
	ownerID int64
	targets map[string]InlineTarget
}

func (r *InlineRouter) SetOwnerID(ownerID int64) { r.mu.Lock(); r.ownerID = ownerID; r.mu.Unlock() }

func NewInlineRouter(max int) (*InlineRouter, error) {
	if max < 1 {
		return nil, ErrLimit
	}
	return &InlineRouter{max: max, targets: make(map[string]InlineTarget)}, nil
}

func (r *InlineRouter) Register(target InlineTarget) error {
	if target.PluginID == "" || target.HandlerID == "" || len(target.QueryPrefix) > 4096 || len(target.SenderIDs) > 1024 || len(target.AllowedUsers) > 1024 {
		return ErrInvalidFilter
	}
	key := eventKey(target.PluginID, target.HandlerID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.targets[key]; exists {
		return ErrConflict
	}
	if len(r.targets) >= r.max {
		return ErrLimit
	}
	target.SenderIDs = slices.Clone(target.SenderIDs)
	target.AllowedUsers = slices.Clone(target.AllowedUsers)
	r.targets[key] = target
	return nil
}

func (r *InlineRouter) UnregisterPlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, target := range r.targets {
		if target.PluginID == pluginID {
			delete(r.targets, key)
		}
	}
}

func (r *InlineRouter) Route(ctx context.Context, query InlineQuery, enqueue func(context.Context, InlineTarget) error) EventRouteResult {
	if enqueue == nil {
		return EventRouteResult{}
	}
	r.mu.Lock()
	targets := make([]InlineTarget, 0, len(r.targets))
	for _, target := range r.targets {
		target.SenderIDs = slices.Clone(target.SenderIDs)
		targets = append(targets, target)
	}
	r.mu.Unlock()
	slices.SortFunc(targets, func(a, b InlineTarget) int {
		if c := strings.Compare(a.PluginID, b.PluginID); c != 0 {
			return c
		}
		return strings.Compare(a.HandlerID, b.HandlerID)
	})
	result := EventRouteResult{}
	lastPlugin := ""
	queued := false
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		if target.PluginID != lastPlugin {
			lastPlugin, queued = target.PluginID, false
		}
		r.mu.Lock()
		ownerID := r.ownerID
		r.mu.Unlock()
		if target.OwnerOnly && query.SenderID != ownerID || target.QueryPrefix != "" && !strings.HasPrefix(query.Query, target.QueryPrefix) || len(target.SenderIDs) > 0 && !slices.Contains(target.SenderIDs, query.SenderID) || len(target.AllowedUsers) > 0 && !slices.Contains(target.AllowedUsers, query.SenderID) {
			continue
		}
		result.Matched++
		if queued {
			continue
		}
		queued = true
		if err := enqueue(ctx, target); err != nil {
			result.Rejected++
		} else {
			result.Enqueued++
		}
	}
	return result
}

type callbackEntry struct {
	target  CallbackTarget
	expires time.Time
}

type CallbackRouter struct {
	mu      sync.Mutex
	max     int
	ownerID int64
	targets map[string]CallbackTarget
	dynamic map[string]callbackEntry
}

func (r *CallbackRouter) SetOwnerID(ownerID int64) { r.mu.Lock(); r.ownerID = ownerID; r.mu.Unlock() }

func NewCallbackRouter(max int) (*CallbackRouter, error) {
	if max < 1 {
		return nil, ErrLimit
	}
	return &CallbackRouter{max: max, targets: make(map[string]CallbackTarget), dynamic: make(map[string]callbackEntry)}, nil
}

func (r *CallbackRouter) Register(target CallbackTarget) error {
	if target.PluginID == "" || target.HandlerID == "" || len(target.Data) > 64 || len(target.DataPrefix) > 64 || len(target.ChatIDs) > 1024 || len(target.SenderIDs) > 1024 || len(target.AllowedUsers) > 1024 {
		return ErrInvalidFilter
	}
	key := eventKey(target.PluginID, target.HandlerID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.targets[key]; exists {
		return ErrConflict
	}
	if len(r.targets) >= r.max {
		return ErrLimit
	}
	target.ChatIDs = slices.Clone(target.ChatIDs)
	target.SenderIDs = slices.Clone(target.SenderIDs)
	target.AllowedUsers = slices.Clone(target.AllowedUsers)
	r.targets[key] = target
	return nil
}

// RegisterDynamic binds a callback payload to a plugin until expiry. It is
// used for action buttons and is swept opportunistically, so it creates no
// goroutine or timer per callback.
func (r *CallbackRouter) RegisterDynamic(data, pluginID, handlerID string, expires time.Time) error {
	if data == "" || len(data) > 64 || pluginID == "" || handlerID == "" || expires.IsZero() {
		return ErrInvalidFilter
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(time.Now())
	if _, exists := r.dynamic[data]; !exists && len(r.dynamic)+len(r.targets) >= r.max {
		return ErrLimit
	}
	r.dynamic[data] = callbackEntry{target: CallbackTarget{PluginID: pluginID, HandlerID: handlerID, Data: data}, expires: expires}
	return nil
}

func (r *CallbackRouter) UnregisterDynamic(data string) {
	r.mu.Lock()
	delete(r.dynamic, data)
	r.mu.Unlock()
}

func (r *CallbackRouter) UnregisterPlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, target := range r.targets {
		if target.PluginID == pluginID {
			delete(r.targets, key)
		}
	}
	for data, entry := range r.dynamic {
		if entry.target.PluginID == pluginID {
			delete(r.dynamic, data)
		}
	}
}

func (r *CallbackRouter) Route(ctx context.Context, callback Callback, enqueue func(context.Context, CallbackTarget) error) EventRouteResult {
	if enqueue == nil {
		return EventRouteResult{}
	}
	r.mu.Lock()
	r.sweepLocked(time.Now())
	targets := make([]CallbackTarget, 0, len(r.targets)+1)
	if entry, ok := r.dynamic[callback.Data]; ok {
		targets = append(targets, entry.target)
	}
	for _, target := range r.targets {
		targets = append(targets, target)
	}
	r.mu.Unlock()
	slices.SortFunc(targets, func(a, b CallbackTarget) int {
		if c := strings.Compare(a.PluginID, b.PluginID); c != 0 {
			return c
		}
		return strings.Compare(a.HandlerID, b.HandlerID)
	})
	result := EventRouteResult{}
	lastPlugin := ""
	queued := false
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		if target.PluginID != lastPlugin {
			lastPlugin, queued = target.PluginID, false
		}
		r.mu.Lock()
		ownerID := r.ownerID
		r.mu.Unlock()
		if target.OwnerOnly && callback.SenderID != ownerID || target.Data != "" && target.Data != callback.Data || target.DataPrefix != "" && !strings.HasPrefix(callback.Data, target.DataPrefix) || len(target.ChatIDs) > 0 && !slices.Contains(target.ChatIDs, callback.ChatID) || len(target.SenderIDs) > 0 && !slices.Contains(target.SenderIDs, callback.SenderID) || len(target.AllowedUsers) > 0 && !slices.Contains(target.AllowedUsers, callback.SenderID) {
			continue
		}
		result.Matched++
		if queued {
			continue
		}
		queued = true
		if err := enqueue(ctx, target); err != nil {
			result.Rejected++
		} else {
			result.Enqueued++
		}
	}
	return result
}

func (r *CallbackRouter) sweepLocked(now time.Time) {
	for data, entry := range r.dynamic {
		if !entry.expires.After(now) {
			delete(r.dynamic, data)
		}
	}
}

func eventKey(pluginID, handlerID string) string { return pluginID + "\x00" + handlerID }
