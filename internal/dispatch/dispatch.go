// Package dispatch parses owner commands and filters watcher updates before enqueue.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidCommand = errors.New("dispatch: invalid command")
	ErrConflict       = errors.New("dispatch: handler conflict")
	ErrLimit          = errors.New("dispatch: configured limit exceeded")
	ErrInvalidFilter  = errors.New("dispatch: invalid watcher filter")
)

type ChatKind string

const (
	ChatUnknown    ChatKind = "unknown"
	ChatPrivate    ChatKind = "private"
	ChatGroup      ChatKind = "group"
	ChatSupergroup ChatKind = "supergroup"
	ChatChannel    ChatKind = "channel"
)

type MediaKind string

const (
	MediaNone     MediaKind = "none"
	MediaPhoto    MediaKind = "photo"
	MediaVideo    MediaKind = "video"
	MediaAudio    MediaKind = "audio"
	MediaDocument MediaKind = "document"
	MediaSticker  MediaKind = "sticker"
	MediaDice     MediaKind = "dice"
	MediaOther    MediaKind = "other"
)

// Message is the Telegram-library-independent subset needed by core dispatch.
type Message struct {
	ID       int64
	ChatID   int64
	SenderID int64
	// UpdateKind preserves the transport-neutral update category so watcher
	// filtering can reject mismatched edits before plugin queue admission.
	UpdateKind string
	Text       string
	Outgoing   bool
	ChatKind   ChatKind
	MediaKind  MediaKind
	ViaBot     bool
	Forwarded  bool
	Reply      bool
	Mentioned  bool
	Editable   bool
	Edited     bool
}

// Command contains a parsed canonical candidate and its original message.
type Command struct {
	Name    string
	Args    string
	Raw     string
	Prefix  string
	Message Message
}

// Parser recognizes only commands authored by the configured account.
type Parser struct {
	prefix string
	selfID int64
}

func NewParser(prefix string, selfID int64) (*Parser, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	if selfID == 0 {
		return nil, fmt.Errorf("dispatch: self ID must be non-zero")
	}
	return &Parser{prefix: prefix, selfID: selfID}, nil
}

func (p *Parser) Prefix() string { return p.prefix }

// Parse returns false for ordinary messages and legacy-ineligible media.
func (p *Parser) Parse(message Message) (Command, bool) {
	if message.SenderID != p.selfID || !strings.HasPrefix(message.Text, p.prefix) ||
		message.ViaBot || message.MediaKind == MediaSticker || message.MediaKind == MediaDice || message.MediaKind == MediaAudio {
		return Command{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(message.Text, p.prefix))
	if rest == "" {
		return Command{}, false
	}
	nameEnd := strings.IndexFunc(rest, unicode.IsSpace)
	name, args := rest, ""
	if nameEnd >= 0 {
		name, args = rest[:nameEnd], strings.TrimSpace(rest[nameEnd:])
	}
	name, err := normalizeCommand(name)
	if err != nil {
		return Command{}, false
	}
	return Command{Name: name, Args: args, Raw: rest, Prefix: p.prefix, Message: message}, true
}

func validatePrefix(prefix string) error {
	if prefix == "" || len(prefix) > 32 || !utf8.ValidString(prefix) {
		return fmt.Errorf("%w: prefix", ErrInvalidCommand)
	}
	for _, r := range prefix {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: prefix", ErrInvalidCommand)
		}
	}
	return nil
}

func normalizeCommand(command string) (string, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || len(command) > 64 || !utf8.ValidString(command) {
		return "", ErrInvalidCommand
	}
	for _, r := range command {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("./\\", r) {
			return "", ErrInvalidCommand
		}
	}
	return command, nil
}

// CommandTarget identifies a handler without exposing an implementation value.
type CommandTarget struct {
	Name      string
	PluginID  string
	HandlerID string
	Core      bool
}

type RegistryLimits struct {
	MaxCommands int
	MaxAliases  int
}

func (l RegistryLimits) normalized() (RegistryLimits, error) {
	if l.MaxCommands == 0 {
		l.MaxCommands = 4096
	}
	if l.MaxAliases == 0 {
		l.MaxAliases = 8192
	}
	if l.MaxCommands < 1 || l.MaxAliases < 1 {
		return RegistryLimits{}, ErrLimit
	}
	return l, nil
}

// CommandRegistry resolves canonical names before aliases and protects core names.
type CommandRegistry struct {
	mu       sync.RWMutex
	limits   RegistryLimits
	commands map[string]CommandTarget
	aliases  map[string]string
}

func NewCommandRegistry(limits RegistryLimits) (*CommandRegistry, error) {
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	return &CommandRegistry{limits: limits, commands: make(map[string]CommandTarget), aliases: make(map[string]string)}, nil
}

// Register atomically registers a target and its declared aliases.
func (r *CommandRegistry) Register(target CommandTarget, aliases ...string) error {
	name, err := normalizeCommand(target.Name)
	if err != nil {
		return err
	}
	if target.PluginID == "" || target.HandlerID == "" {
		return fmt.Errorf("%w: target identity", ErrInvalidCommand)
	}
	normalizedAliases := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		a, err := normalizeCommand(alias)
		if err != nil {
			return fmt.Errorf("dispatch: alias %q: %w", alias, err)
		}
		if a == name {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		normalizedAliases = append(normalizedAliases, a)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.commands[name]; ok && (existing.PluginID != target.PluginID || existing.HandlerID != target.HandlerID) {
		return fmt.Errorf("%w: command %q", ErrConflict, name)
	}
	if existing, ok := r.commands[name]; ok && existing.Core && !target.Core {
		return fmt.Errorf("%w: core command %q", ErrConflict, name)
	}
	if _, exists := r.commands[name]; !exists && len(r.commands) >= r.limits.MaxCommands {
		return fmt.Errorf("%w: commands", ErrLimit)
	}
	for _, alias := range normalizedAliases {
		if canonical, ok := r.aliases[alias]; ok && canonical != name {
			return fmt.Errorf("%w: alias %q", ErrConflict, alias)
		}
		if command, ok := r.commands[alias]; ok && command.Name != name {
			return fmt.Errorf("%w: alias shadows command %q", ErrConflict, alias)
		}
	}
	newAliases := 0
	for _, alias := range normalizedAliases {
		if _, exists := r.aliases[alias]; !exists {
			newAliases++
		}
	}
	if len(r.aliases)+newAliases > r.limits.MaxAliases {
		return fmt.Errorf("%w: aliases", ErrLimit)
	}
	target.Name = name
	r.commands[name] = target
	for _, alias := range normalizedAliases {
		r.aliases[alias] = name
	}
	return nil
}

// Resolve applies exact-name precedence, then aliases.
func (r *CommandRegistry) Resolve(name string) (CommandTarget, bool) {
	name, err := normalizeCommand(name)
	if err != nil {
		return CommandTarget{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if target, ok := r.commands[name]; ok {
		return target, true
	}
	canonical, ok := r.aliases[name]
	if !ok {
		return CommandTarget{}, false
	}
	target, ok := r.commands[canonical]
	return target, ok
}

// AddAlias adds a persisted alias for an existing canonical command.
func (r *CommandRegistry) AddAlias(alias, canonical string) error {
	alias, err := normalizeCommand(alias)
	if err != nil {
		return fmt.Errorf("dispatch: alias: %w", err)
	}
	canonical, err = normalizeCommand(canonical)
	if err != nil {
		return fmt.Errorf("dispatch: canonical command: %w", err)
	}
	if alias == canonical {
		return fmt.Errorf("%w: alias equals canonical command", ErrConflict)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.commands[canonical]; !ok {
		return fmt.Errorf("%w: canonical command %q is not registered", ErrInvalidCommand, canonical)
	}
	if _, ok := r.commands[alias]; ok {
		return fmt.Errorf("%w: alias shadows exact command %q", ErrConflict, alias)
	}
	if existing, ok := r.aliases[alias]; ok {
		if existing == canonical {
			return nil
		}
		return fmt.Errorf("%w: alias %q", ErrConflict, alias)
	}
	if len(r.aliases) >= r.limits.MaxAliases {
		return fmt.Errorf("%w: aliases", ErrLimit)
	}
	r.aliases[alias] = canonical
	return nil
}

// RemoveAlias removes an alias without affecting its canonical handler.
func (r *CommandRegistry) RemoveAlias(alias string) bool {
	alias, err := normalizeCommand(alias)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.aliases[alias]; !ok {
		return false
	}
	delete(r.aliases, alias)
	return true
}

// Aliases returns a detached copy suitable for settings and diagnostics.
func (r *CommandRegistry) Aliases() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]string, len(r.aliases))
	for alias, canonical := range r.aliases {
		result[alias] = canonical
	}
	return result
}

// Commands returns a stable help-catalog snapshot sorted by canonical name.
func (r *CommandRegistry) Commands() []CommandTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]CommandTarget, 0, len(r.commands))
	for _, target := range r.commands {
		result = append(result, target)
	}
	slices.SortFunc(result, func(a, b CommandTarget) int { return strings.Compare(a.Name, b.Name) })
	return result
}

func (r *CommandRegistry) UnregisterPlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := make(map[string]struct{})
	for name, target := range r.commands {
		if target.PluginID == pluginID {
			delete(r.commands, name)
			removed[name] = struct{}{}
		}
	}
	for alias, canonical := range r.aliases {
		if _, ok := removed[canonical]; ok {
			delete(r.aliases, alias)
		}
	}
}

func (r *CommandRegistry) Counts() (commands, aliases int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.commands), len(r.aliases)
}

type MatchMode uint8

const (
	MatchAny MatchMode = iota
	MatchRequired
	MatchRejected
)

type Direction uint8

const (
	DirectionAny Direction = iota
	DirectionIncoming
	DirectionOutgoing
)

type CommandMode uint8

const (
	CommandsAny CommandMode = iota
	CommandsOnly
	CommandsExclude
)

// WatcherSpec is a serializable declaration compiled once at registration.
type WatcherSpec struct {
	UpdateKinds       []string
	Direction         Direction
	ChatKinds         []ChatKind
	ExcludeChatKinds  []ChatKind
	MediaKinds        []MediaKind
	ExcludeMediaKinds []MediaKind
	Forwarded         MatchMode
	Reply             MatchMode
	Mentioned         MatchMode
	ViaBot            MatchMode
	Editable          MatchMode
	TextPrefix        string
	TextSuffix        string
	TextContains      string
	TextRegex         string
	SenderIDs         []int64
	ChatIDs           []int64
	Commands          CommandMode
}

// CompiledWatcher is immutable and safe for concurrent Match calls.
type CompiledWatcher struct {
	spec   WatcherSpec
	regex  *regexp.Regexp
	parser *Parser
}

func CompileWatcher(spec WatcherSpec, parser *Parser) (*CompiledWatcher, error) {
	if spec.Direction > DirectionOutgoing || spec.Commands > CommandsExclude ||
		spec.Forwarded > MatchRejected || spec.Reply > MatchRejected || spec.Mentioned > MatchRejected ||
		spec.ViaBot > MatchRejected || spec.Editable > MatchRejected {
		return nil, ErrInvalidFilter
	}
	if spec.Commands != CommandsAny && parser == nil {
		return nil, fmt.Errorf("%w: command mode needs parser", ErrInvalidFilter)
	}
	if len(spec.UpdateKinds) > 8 || len(spec.ChatKinds) > 8 || len(spec.ExcludeChatKinds) > 8 || len(spec.MediaKinds) > 16 ||
		len(spec.ExcludeMediaKinds) > 16 || len(spec.SenderIDs) > 1024 || len(spec.ChatIDs) > 1024 {
		return nil, fmt.Errorf("%w: collection bound", ErrInvalidFilter)
	}
	for _, kind := range spec.UpdateKinds {
		if kind == "" || len(kind) > 64 {
			return nil, fmt.Errorf("%w: update kind", ErrInvalidFilter)
		}
	}
	if len(spec.TextPrefix) > 4096 || len(spec.TextSuffix) > 4096 || len(spec.TextContains) > 4096 || len(spec.TextRegex) > 4096 {
		return nil, fmt.Errorf("%w: text expression", ErrInvalidFilter)
	}
	c := &CompiledWatcher{spec: spec, parser: parser}
	if spec.TextRegex != "" {
		re, err := regexp.Compile(spec.TextRegex)
		if err != nil {
			return nil, fmt.Errorf("%w: regex: %v", ErrInvalidFilter, err)
		}
		c.regex = re
	}
	return c, nil
}

func modeMatches(mode MatchMode, value bool) bool {
	switch mode {
	case MatchRequired:
		return value
	case MatchRejected:
		return !value
	default:
		return true
	}
}

// Match applies every declared filter without allocation or plugin invocation.
func (f *CompiledWatcher) Match(message Message) bool {
	s := &f.spec
	if len(s.UpdateKinds) > 0 && !slices.Contains(s.UpdateKinds, message.UpdateKind) {
		return false
	}
	if s.Direction == DirectionIncoming && message.Outgoing || s.Direction == DirectionOutgoing && !message.Outgoing {
		return false
	}
	if len(s.ChatKinds) > 0 && !slices.Contains(s.ChatKinds, message.ChatKind) || slices.Contains(s.ExcludeChatKinds, message.ChatKind) {
		return false
	}
	if len(s.MediaKinds) > 0 && !slices.Contains(s.MediaKinds, message.MediaKind) || slices.Contains(s.ExcludeMediaKinds, message.MediaKind) {
		return false
	}
	if !modeMatches(s.Forwarded, message.Forwarded) || !modeMatches(s.Reply, message.Reply) || !modeMatches(s.Mentioned, message.Mentioned) || !modeMatches(s.ViaBot, message.ViaBot) || !modeMatches(s.Editable, message.Editable) {
		return false
	}
	if s.TextPrefix != "" && !strings.HasPrefix(message.Text, s.TextPrefix) || s.TextSuffix != "" && !strings.HasSuffix(message.Text, s.TextSuffix) || s.TextContains != "" && !strings.Contains(message.Text, s.TextContains) {
		return false
	}
	if f.regex != nil && !f.regex.MatchString(message.Text) {
		return false
	}
	if len(s.SenderIDs) > 0 && !slices.Contains(s.SenderIDs, message.SenderID) || len(s.ChatIDs) > 0 && !slices.Contains(s.ChatIDs, message.ChatID) {
		return false
	}
	if s.Commands != CommandsAny {
		_, command := f.parser.Parse(message)
		if s.Commands == CommandsOnly && !command || s.Commands == CommandsExclude && command {
			return false
		}
	}
	return true
}

// WatcherTarget carries a precompiled filter and bounded routing identity.
type WatcherTarget struct {
	PluginID  string
	HandlerID string
	Filter    *CompiledWatcher
}

type DisableRule struct {
	All      bool
	Chats    []int64
	Incoming bool
	Outgoing bool
	Private  bool
	Public   bool
}

// WatcherPolicy captures global and per-plugin watcher controls.
type WatcherPolicy struct {
	BlockedChats   []int64
	AllowedChats   []int64
	AllowedPlugins []string
	Disabled       map[string]DisableRule
}

type RouteResult struct{ Matched, Enqueued, Rejected int }

// WatcherRouter owns a fixed maximum number of watcher registrations.
type WatcherRouter struct {
	mu       sync.Mutex
	max      int
	targets  map[string]WatcherTarget
	policy   WatcherPolicy
	snapshot atomic.Pointer[watcherSnapshot]
}

type watcherSnapshot struct {
	targets []WatcherTarget
	policy  WatcherPolicy
}

func NewWatcherRouter(maxWatchers int) (*WatcherRouter, error) {
	if maxWatchers < 1 {
		return nil, fmt.Errorf("%w: watchers", ErrLimit)
	}
	router := &WatcherRouter{max: maxWatchers, targets: make(map[string]WatcherTarget)}
	router.snapshot.Store(&watcherSnapshot{})
	return router, nil
}

func watcherKey(pluginID, handlerID string) string { return pluginID + "\x00" + handlerID }

func (r *WatcherRouter) Register(target WatcherTarget) error {
	if target.PluginID == "" || target.HandlerID == "" || target.Filter == nil {
		return ErrInvalidFilter
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := watcherKey(target.PluginID, target.HandlerID)
	if _, exists := r.targets[key]; !exists && len(r.targets) >= r.max {
		return fmt.Errorf("%w: watchers", ErrLimit)
	}
	r.targets[key] = target
	r.rebuildSnapshotLocked()
	return nil
}

func (r *WatcherRouter) UnregisterPlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, target := range r.targets {
		if target.PluginID == pluginID {
			delete(r.targets, key)
		}
	}
	r.rebuildSnapshotLocked()
}

func (r *WatcherRouter) SetPolicy(policy WatcherPolicy) error {
	if len(policy.BlockedChats) > r.max*4 || len(policy.AllowedChats) > r.max*4 || len(policy.AllowedPlugins) > r.max || len(policy.Disabled) > r.max {
		return fmt.Errorf("%w: watcher policy", ErrLimit)
	}
	copyPolicy := WatcherPolicy{BlockedChats: slices.Clone(policy.BlockedChats), AllowedChats: slices.Clone(policy.AllowedChats), AllowedPlugins: slices.Clone(policy.AllowedPlugins), Disabled: make(map[string]DisableRule, len(policy.Disabled))}
	for plugin, rule := range policy.Disabled {
		rule.Chats = slices.Clone(rule.Chats)
		copyPolicy.Disabled[plugin] = rule
	}
	r.mu.Lock()
	r.policy = copyPolicy
	r.rebuildSnapshotLocked()
	r.mu.Unlock()
	return nil
}

func (r *WatcherRouter) rebuildSnapshotLocked() {
	targets := make([]WatcherTarget, 0, len(r.targets))
	for _, target := range r.targets {
		targets = append(targets, target)
	}
	slices.SortFunc(targets, func(left, right WatcherTarget) int {
		if compared := strings.Compare(left.PluginID, right.PluginID); compared != 0 {
			return compared
		}
		return strings.Compare(left.HandlerID, right.HandlerID)
	})
	r.snapshot.Store(&watcherSnapshot{targets: targets, policy: r.policy})
}

func policyAllows(policy WatcherPolicy, target WatcherTarget, message Message) bool {
	if slices.Contains(policy.BlockedChats, message.ChatID) || len(policy.AllowedChats) > 0 && !slices.Contains(policy.AllowedChats, message.ChatID) || len(policy.AllowedPlugins) > 0 && !slices.Contains(policy.AllowedPlugins, target.PluginID) {
		return false
	}
	rule, ok := policy.Disabled[target.PluginID]
	if !ok {
		return true
	}
	public := message.ChatKind != ChatPrivate
	return !(rule.All || slices.Contains(rule.Chats, message.ChatID) || rule.Incoming && !message.Outgoing || rule.Outgoing && message.Outgoing || rule.Private && !public || rule.Public && public)
}

// Route calls enqueue only for targets that passed global and handler filters.
func (r *WatcherRouter) Route(ctx context.Context, message Message, enqueue func(context.Context, WatcherTarget, Message) error) RouteResult {
	if enqueue == nil {
		return RouteResult{}
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return RouteResult{}
	}
	result := RouteResult{}
	// Protocol dispatch carries one immutable update per plugin. A plugin may
	// declare several watcher filters, but it performs its own final handler
	// selection after the core's filters admit the update. Enqueueing once per
	// matching handler would multiply side effects when filters overlap.
	lastPlugin := ""
	attempted := false
	for _, target := range snapshot.targets {
		if ctx.Err() != nil {
			break
		}
		if target.PluginID != lastPlugin {
			lastPlugin = target.PluginID
			attempted = false
		}
		if !policyAllows(snapshot.policy, target, message) || !target.Filter.Match(message) {
			continue
		}
		result.Matched++
		if attempted {
			continue
		}
		attempted = true
		if err := enqueue(ctx, target, message); err != nil {
			result.Rejected++
		} else {
			result.Enqueued++
		}
	}
	return result
}
