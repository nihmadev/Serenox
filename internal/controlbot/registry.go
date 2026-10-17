package controlbot

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrCommandConflict     = errors.New("control command conflict")
	ErrCommandUnauthorized = errors.New("control command actor is not authorized")
	ErrCommandSurface      = errors.New("control command is not exposed on this surface")
	ErrCommandConfirmation = errors.New("control command confirmation is required")
)

type CommandTarget struct {
	PluginID string
	Command  pluginv1.ControlCommand
}

// Registry is a bounded, atomic control-command directory. It intentionally
// doesn't reuse the user-session command registry: names may overlap without
// exposing an owner-authored command to the bot.
type Registry struct {
	mu       sync.RWMutex
	ownerID  int64
	max      int
	commands map[string]CommandTarget
	plugins  map[string][]string
}

func NewRegistry(ownerID int64, max int) (*Registry, error) {
	if ownerID == 0 || max < 1 || max > pluginv1.MaxControlCommands {
		return nil, errors.New("invalid control command registry")
	}
	return &Registry{ownerID: ownerID, max: max, commands: make(map[string]CommandTarget), plugins: make(map[string][]string)}, nil
}
func commandKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Register validates all declarations before mutating registry state, so a
// plugin load either publishes every declared command or none of them.
func (r *Registry) Register(pluginID string, declarations []pluginv1.ControlCommand, capabilities []pluginv1.Capability) error {
	if pluginID == "" {
		return errors.New("control command plugin ID is required")
	}
	if len(declarations) > pluginv1.MaxControlCommands {
		return errors.New("too many control commands")
	}
	if err := (pluginv1.PluginMetadata{ID: "validation", Name: "validation", Version: "1", ProtocolVersion: pluginv1.Version, Capabilities: capabilities, ControlCommands: declarations}).Validate(); err != nil {
		return err
	}
	hasBot := false
	for _, c := range capabilities {
		if c == pluginv1.CapabilityControlBot {
			hasBot = true
		}
	}
	if !hasBot && len(declarations) > 0 {
		return errors.New("control commands require control_bot capability")
	}
	staged := make(map[string]CommandTarget, len(declarations))
	for _, declaration := range declarations {
		target := CommandTarget{PluginID: pluginID, Command: declaration}
		names := append([]string{declaration.Name}, declaration.Aliases...)
		for _, name := range names {
			key := commandKey(name)
			if _, ok := staged[key]; ok {
				return ErrCommandConflict
			}
			staged[key] = target
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[pluginID]; exists {
		return ErrCommandConflict
	}
	if len(r.commands)+len(staged) > r.max {
		return errors.New("control command registry capacity reached")
	}
	for key := range staged {
		if _, exists := r.commands[key]; exists {
			return ErrCommandConflict
		}
	}
	keys := make([]string, 0, len(staged))
	for key, target := range staged {
		r.commands[key] = target
		keys = append(keys, key)
	}
	r.plugins[pluginID] = keys
	return nil
}
func (r *Registry) UnregisterPlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range r.plugins[pluginID] {
		delete(r.commands, key)
	}
	delete(r.plugins, pluginID)
}
func (r *Registry) Resolve(name string, actorID int64, surface pluginv1.ControlSurface, argument string) (CommandTarget, error) {
	r.mu.RLock()
	target, ok := r.commands[commandKey(name)]
	owner := r.ownerID
	r.mu.RUnlock()
	if !ok {
		return CommandTarget{}, ErrCommandSurface
	}
	decl := target.Command
	found := false
	for _, candidate := range decl.Surfaces {
		if candidate == surface {
			found = true
		}
	}
	if !found {
		return CommandTarget{}, ErrCommandSurface
	}
	if len(argument) > decl.MaxArgumentBytes && decl.MaxArgumentBytes > 0 {
		return CommandTarget{}, errors.New("control command arguments exceed declaration")
	}
	if decl.ActorPolicy == pluginv1.ControlActorAllowList {
		found = false
		for _, id := range decl.AllowedActorIDs {
			if id == actorID {
				found = true
			}
		}
		if !found {
			return CommandTarget{}, ErrCommandUnauthorized
		}
	} else if actorID != owner {
		return CommandTarget{}, ErrCommandUnauthorized
	}
	if surface == pluginv1.ControlSurfaceInline && decl.Mutating {
		return CommandTarget{}, ErrCommandConfirmation
	}
	return target, nil
}
func (r *Registry) Commands() []CommandTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]CommandTarget, 0, len(r.commands))
	seen := map[string]struct{}{}
	for _, target := range r.commands {
		key := target.PluginID + "\x00" + target.Command.Name
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, target)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Command.Name < result[j].Command.Name })
	return result
}

// FlowStore owns bounded confirmation/flow state. Sweep is called by a shared
// lifecycle ticker (or on access), never by a timer per flow.
type Flow struct {
	ID                        string
	AccountID, BotID, ActorID int64
	PluginID                  string
	ChatID                    int64
	CreatedAt, ExpiresAt      time.Time
	Steps                     int
	State                     []byte
}
type FlowStore struct {
	mu                      sync.Mutex
	max, maxState, maxSteps int
	flows                   map[string]Flow
}

func NewFlowStore(max, maxState, maxSteps int) (*FlowStore, error) {
	if max < 1 || maxState < 1 || maxState > pluginv1.MaxControlStateBytes || maxSteps < 1 || maxSteps > pluginv1.MaxControlFlowSteps {
		return nil, errors.New("invalid control flow limits")
	}
	return &FlowStore{max: max, maxState: maxState, maxSteps: maxSteps, flows: make(map[string]Flow, max)}, nil
}
func (s *FlowStore) Create(accountID, botID, actorID, chatID int64, pluginID string, state []byte, ttl time.Duration) (Flow, error) {
	if accountID == 0 || botID == 0 || actorID == 0 || pluginID == "" || len(state) > s.maxState || ttl <= 0 || ttl > 24*time.Hour {
		return Flow{}, errors.New("invalid control flow")
	}
	id, err := NewOpaqueID()
	if err != nil {
		return Flow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	if len(s.flows) >= s.max {
		return Flow{}, errors.New("control flow capacity reached")
	}
	flow := Flow{ID: id, AccountID: accountID, BotID: botID, ActorID: actorID, PluginID: pluginID, ChatID: chatID, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(ttl), State: append([]byte(nil), state...)}
	s.flows[id] = flow
	return flow, nil
}
func (s *FlowStore) Consume(id, pluginID string, accountID, botID, actorID, chatID int64) (Flow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	flow, ok := s.flows[id]
	if !ok {
		return Flow{}, errors.New("control flow expired")
	}
	if flow.PluginID != pluginID || flow.AccountID != accountID || flow.BotID != botID || flow.ActorID != actorID || flow.ChatID != chatID {
		return Flow{}, ErrCommandUnauthorized
	}
	if flow.Steps >= s.maxSteps {
		return Flow{}, errors.New("control flow step limit reached")
	}
	flow.Steps++
	s.flows[id] = flow
	return cloneFlow(flow), nil
}
func (s *FlowStore) Complete(id string) { s.mu.Lock(); delete(s.flows, id); s.mu.Unlock() }
func (s *FlowStore) ClearPlugin(pluginID string) {
	s.mu.Lock()
	for id, flow := range s.flows {
		if flow.PluginID == pluginID {
			delete(s.flows, id)
		}
	}
	s.mu.Unlock()
}
func (s *FlowStore) Clear()              { s.mu.Lock(); clear(s.flows); s.mu.Unlock() }
func (s *FlowStore) Sweep(now time.Time) { s.mu.Lock(); s.sweepLocked(now); s.mu.Unlock() }
func (s *FlowStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	return len(s.flows)
}
func (s *FlowStore) sweepLocked(now time.Time) {
	for id, flow := range s.flows {
		if !flow.ExpiresAt.After(now) {
			delete(s.flows, id)
		}
	}
}
func cloneFlow(flow Flow) Flow { flow.State = append([]byte(nil), flow.State...); return flow }
