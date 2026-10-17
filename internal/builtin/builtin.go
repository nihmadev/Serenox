// Package builtin implements essential core modules as ordinary bounded Go
// handlers: loader, help, settings, updater, and diagnostics.
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/modules"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/ratelimit"
	"github.com/nihmadev/Serenox/internal/storage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var ErrCommandFailureFixture = errors.New("diagnostics failure fixture")

type PluginStatus struct {
	ID                      string
	Version                 string
	State                   string
	External                bool
	Category                string
	InterpreterPath         string
	InterpreterVersion      string
	SidecarVersion          string
	SDKVersion              string
	ProtocolVersion         string
	PackageVersion          string
	ProcessState            string
	RestartCount            uint64
	CircuitState            string
	QueueDepth              int
	WorkerCount             int
	RSSBytes                uint64
	LastError               string
	UnsupportedCapabilities []string
}

type PluginDiagnostics interface {
	Diagnostics() []PluginStatus
}

type PluginController interface {
	List() []PluginStatus
	Load(context.Context, string) error
	Unload(context.Context, string) error
}

type Services struct {
	Store          storage.Store
	Plugins        PluginController
	SourceURL      string
	StartedAt      time.Time
	Prefix         func() string
	SetPrefix      func(string) error
	RequestRestart func(context.Context, bool) error
	Resources      func() observability.Resources
	RateStats      func() ratelimit.Stats
	ExecutorStats  func() map[string]executor.Stats
	ModuleStatuses func() []modules.Status
}

type ActionSink func(context.Context, []pluginv1.Action) error

type CommandDefinition struct {
	Name        string
	Aliases     []string
	Description string
	Handler     func(context.Context, invocation) ([]pluginv1.Action, error)
}

type Definition struct {
	ID          string
	Name        string
	Description string
	Commands    []CommandDefinition
}

type invocation struct {
	request    pluginv1.DispatchRequest
	command    pluginv1.Command
	receivedAt time.Time
}

type Catalog struct {
	services Services
	sink     ActionSink
	defs     []Definition
	byID     map[string]Definition
	actionID atomic.Uint64
	mu       sync.Mutex
	started  bool
	pools    map[string]*executor.Pool
	commands *dispatch.CommandRegistry
}

func New(services Services, sink ActionSink) (*Catalog, error) {
	if services.Store == nil || sink == nil || services.Prefix == nil || services.SetPrefix == nil {
		return nil, errors.New("builtin services are incomplete")
	}
	if services.StartedAt.IsZero() {
		services.StartedAt = time.Now()
	}
	if services.SourceURL == "" {
		services.SourceURL = "https://github.com/nihmadev/Serenox"
	}
	catalog := &Catalog{services: services, sink: sink, pools: make(map[string]*executor.Pool)}
	catalog.defs = catalog.buildDefinitions()
	catalog.byID = make(map[string]Definition, len(catalog.defs))
	for _, definition := range catalog.defs {
		catalog.byID[definition.ID] = definition
	}
	return catalog, nil
}

func (c *Catalog) Definitions() []Definition {
	result := make([]Definition, len(c.defs))
	for index, definition := range c.defs {
		result[index] = definition
		result[index].Commands = append([]CommandDefinition(nil), definition.Commands...)
	}
	return result
}

// Start registers one fixed worker pool per built-in module and then its command
// names. Any partial registration is rolled back.
func (c *Catalog) Start(ctx context.Context, manager *executor.Manager, commands *dispatch.CommandRegistry, config executor.Config) error {
	if manager == nil || commands == nil {
		return errors.New("nil built-in runtime")
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("built-ins already started")
	}
	c.started = true
	c.commands = commands
	c.mu.Unlock()

	registered := make([]string, 0, len(c.defs))
	rollback := func() {
		for index := len(registered) - 1; index >= 0; index-- {
			id := registered[index]
			commands.UnregisterPlugin(id)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = manager.Unregister(shutdownCtx, id)
			cancel()
		}
		c.mu.Lock()
		c.started = false
		c.pools = make(map[string]*executor.Pool)
		c.mu.Unlock()
	}
	for _, definition := range c.defs {
		definition := definition
		pool, err := manager.Register(definition.ID, config, func(handlerCtx context.Context, job executor.Job) error {
			return c.handle(handlerCtx, definition, job)
		}, executor.Hooks{})
		if err != nil {
			rollback()
			return fmt.Errorf("start built-in %s: %w", definition.ID, err)
		}
		c.mu.Lock()
		c.pools[definition.ID] = pool
		c.mu.Unlock()
		registered = append(registered, definition.ID)
		for _, command := range definition.Commands {
			target := dispatch.CommandTarget{Name: command.Name, PluginID: definition.ID, HandlerID: command.Name, Core: true}
			if err := commands.Register(target, command.Aliases...); err != nil {
				rollback()
				return fmt.Errorf("register built-in command %s: %w", command.Name, err)
			}
		}
	}
	for alias, canonical := range c.loadAliases(ctx) {
		if err := commands.AddAlias(alias, canonical); err != nil {
			rollback()
			return fmt.Errorf("restore alias %s: %w", alias, err)
		}
	}
	return nil
}

func (c *Catalog) Stop(ctx context.Context, manager *executor.Manager) error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return nil
	}
	c.started = false
	commands := c.commands
	ids := make([]string, 0, len(c.pools))
	for id := range c.pools {
		ids = append(ids, id)
	}
	c.pools = make(map[string]*executor.Pool)
	c.mu.Unlock()
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	var joined error
	for _, id := range ids {
		commands.UnregisterPlugin(id)
		if err := manager.Unregister(ctx, id); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (c *Catalog) handle(ctx context.Context, definition Definition, job executor.Job) error {
	request, ok := job.Payload.(pluginv1.DispatchRequest)
	if !ok || request.Command == nil {
		return errors.New("built-in received invalid command payload")
	}
	for _, command := range definition.Commands {
		if command.Name == job.HandlerID {
			actions, err := command.Handler(ctx, invocation{request: request, command: *request.Command, receivedAt: time.Now()})
			if err != nil {
				return err
			}
			for index := range actions {
				if actions[index].ID == "" {
					actions[index].ID = fmt.Sprintf("builtin-%d", c.actionID.Add(1))
				}
			}
			return c.sink(ctx, actions)
		}
	}
	return fmt.Errorf("unknown built-in handler %s", job.HandlerID)
}

func (c *Catalog) buildDefinitions() []Definition {
	return []Definition{
		{
			ID: "core.loader", Name: "Loader", Description: "Install and run verified .srnx plugins",
			Commands: []CommandDefinition{
				{Name: "loadmod", Aliases: []string{"lm", "dlmod", "dlm"}, Description: "Verify, install, and start a .srnx package", Handler: c.loadPlugin},
				{Name: "unloadmod", Aliases: []string{"ulm"}, Description: "Stop a plugin while retaining its installed package", Handler: c.unloadPlugin},
				{Name: "plugins", Description: "List loaded plugins", Handler: c.listPlugins},
				{Name: "clearmodules", Description: "Unload every external plugin", Handler: c.clearPlugins},
			},
		},
		{
			ID: "core.help", Name: "Help", Description: "List modules and command help",
			Commands: []CommandDefinition{
				{Name: "help", Description: "Show command help", Handler: c.help},
				{Name: "helphide", Description: "Hide or show a module in help", Handler: c.helpHide},
			},
		},
		{
			ID: "core.settings", Name: "Settings", Description: "Configure prefix and aliases",
			Commands: []CommandDefinition{
				{Name: "setprefix", Description: "Set the command prefix", Handler: c.setPrefix},
				{Name: "aliases", Description: "List aliases", Handler: c.aliases},
				{Name: "addalias", Description: "Add a command alias", Handler: c.addAlias},
				{Name: "delalias", Description: "Delete a command alias", Handler: c.delAlias},
				{Name: "watchers", Description: "Show watcher state", Handler: c.watchers},
			},
		},
		{
			ID: "core.updater", Name: "Updater", Description: "Controlled restart and source information",
			Commands: []CommandDefinition{
				{Name: "restart", Description: "Request a graceful restart", Handler: c.restart},
				{Name: "source", Description: "Show source URL", Handler: c.source},
			},
		},
		{
			ID: "core.diagnostics", Name: "Diagnostics", Description: "Runtime health and resource usage",
			Commands: []CommandDefinition{
				{Name: "ping", Description: "Measure command latency and uptime", Handler: c.ping},
				{Name: "diagnostics", Aliases: []string{"diag"}, Description: "Show bounded runtime metrics", Handler: c.diagnostics},
				{Name: "fail", Description: "Exercise command error recovery", Handler: c.fail},
			},
		},
	}
}

func (c *Catalog) reply(inv invocation, text string) []pluginv1.Action {
	return []pluginv1.Action{{
		Kind: pluginv1.ActionEdit, ChatID: inv.command.Message.ChatID,
		MessageID: inv.command.Message.ID, Text: text, ParseMode: "HTML",
	}}
}

func (c *Catalog) loadPlugin(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	path := strings.TrimSpace(inv.command.RawArgs)
	if path == "" {
		return c.reply(inv, "Usage: <code>loadmod /path/to/plugin.srnx</code>"), nil
	}
	if c.services.Plugins == nil {
		return c.reply(inv, "External plugin supervision is not configured."), nil
	}
	if err := c.services.Plugins.Load(ctx, path); err != nil {
		return c.reply(inv, "Plugin load failed: <code>"+escape(err.Error())+"</code>"), nil
	}
	return c.reply(inv, "Plugin package verified, installed, and started."), nil
}

func (c *Catalog) unloadPlugin(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	id := strings.TrimSpace(inv.command.RawArgs)
	if id == "" {
		return c.reply(inv, "Usage: <code>unloadmod plugin-id</code>"), nil
	}
	if strings.HasPrefix(id, "core.") {
		return c.reply(inv, "Core modules are protected."), nil
	}
	if c.services.Plugins == nil {
		return c.reply(inv, "External plugin supervision is not configured."), nil
	}
	if err := c.services.Plugins.Unload(ctx, id); err != nil {
		return c.reply(inv, "Plugin unload failed: <code>"+escape(err.Error())+"</code>"), nil
	}
	return c.reply(inv, "Plugin stopped; its verified package remains installed."), nil
}

func (c *Catalog) listPlugins(_ context.Context, inv invocation) ([]pluginv1.Action, error) {
	lines := []string{"<b>Loaded plugins</b>"}
	for _, definition := range c.defs {
		lines = append(lines, "• <code>"+definition.ID+"</code> — ready (built-in)")
	}
	if c.services.Plugins != nil {
		statuses := c.services.Plugins.List()
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
		for _, status := range statuses {
			lines = append(lines, fmt.Sprintf("• <code>%s</code> — %s", escape(status.ID), escape(status.State)))
		}
	}
	return c.reply(inv, strings.Join(lines, "\n")), nil
}

func (c *Catalog) clearPlugins(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	if c.services.Plugins == nil {
		return c.reply(inv, "No external plugins are configured."), nil
	}
	removed := 0
	for _, status := range c.services.Plugins.List() {
		if !status.External || strings.HasPrefix(status.ID, "core.") {
			continue
		}
		if err := c.services.Plugins.Unload(ctx, status.ID); err != nil {
			return c.reply(inv, fmt.Sprintf("Stopped after %d plugins: <code>%s</code>", removed, escape(err.Error()))), nil
		}
		removed++
	}
	return c.reply(inv, fmt.Sprintf("Unloaded %d external plugins.", removed)), nil
}

func (c *Catalog) help(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	hidden := c.hidden(ctx)
	query := strings.ToLower(strings.TrimSpace(inv.command.RawArgs))
	if query != "" {
		for _, definition := range c.defs {
			if strings.EqualFold(query, definition.Name) || query == definition.ID {
				return c.reply(inv, renderModuleHelp(definition)), nil
			}
			for _, command := range definition.Commands {
				if query == command.Name || containsFold(command.Aliases, query) {
					return c.reply(inv, renderModuleHelp(definition)), nil
				}
			}
		}
		return c.reply(inv, "Module or command not found."), nil
	}
	definitions := append([]Definition(nil), c.defs...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	lines := []string{"<b>Serenox modules</b>"}
	for _, definition := range definitions {
		if hidden[definition.ID] {
			continue
		}
		commands := make([]string, 0, len(definition.Commands))
		for _, command := range definition.Commands {
			commands = append(commands, c.services.Prefix()+command.Name)
		}
		lines = append(lines, fmt.Sprintf("• <b>%s</b>: <code>%s</code>", escape(definition.Name), strings.Join(commands, "</code>, <code>")))
	}
	return c.reply(inv, strings.Join(lines, "\n")), nil
}

func (c *Catalog) helpHide(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	query := strings.TrimSpace(inv.command.RawArgs)
	definition, exists := c.byID[query]
	if !exists {
		for _, candidate := range c.defs {
			if strings.EqualFold(candidate.Name, query) {
				definition, exists = candidate, true
				break
			}
		}
	}
	if !exists {
		return c.reply(inv, "Usage: <code>helphide module</code>"), nil
	}
	hidden := c.hidden(ctx)
	hidden[definition.ID] = !hidden[definition.ID]
	payload, _ := json.Marshal(hidden)
	if err := c.services.Store.Set(ctx, "core.help", "hidden", payload); err != nil {
		return nil, err
	}
	state := "shown"
	if hidden[definition.ID] {
		state = "hidden"
	}
	return c.reply(inv, fmt.Sprintf("<b>%s</b> is now %s.", escape(definition.Name), state)), nil
}

func (c *Catalog) hidden(ctx context.Context) map[string]bool {
	hidden := make(map[string]bool)
	payload, found, err := c.services.Store.Get(ctx, "core.help", "hidden")
	if err == nil && found {
		_ = json.Unmarshal(payload, &hidden)
	}
	return hidden
}

func (c *Catalog) setPrefix(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	prefix := strings.TrimSpace(inv.command.RawArgs)
	if utf8.RuneCountInString(prefix) != 1 || prefix == "s" {
		return c.reply(inv, "Prefix must be exactly one character and cannot be <code>s</code>."), nil
	}
	if err := c.services.SetPrefix(prefix); err != nil {
		return c.reply(inv, "Prefix rejected: <code>"+escape(err.Error())+"</code>"), nil
	}
	payload, _ := json.Marshal(prefix)
	if err := c.services.Store.Set(ctx, "core.settings", "command_prefix", payload); err != nil {
		return nil, err
	}
	return c.reply(inv, "Command prefix updated to <code>"+escape(prefix)+"</code>."), nil
}

func (c *Catalog) aliases(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	aliases := c.loadAliases(ctx)
	if len(aliases) == 0 {
		return c.reply(inv, "No aliases configured."), nil
	}
	keys := make([]string, 0, len(aliases))
	for alias := range aliases {
		keys = append(keys, alias)
	}
	sort.Strings(keys)
	lines := []string{"<b>Aliases</b>"}
	for _, alias := range keys {
		lines = append(lines, fmt.Sprintf("• <code>%s</code> → <code>%s</code>", escape(alias), escape(aliases[alias])))
	}
	return c.reply(inv, strings.Join(lines, "\n")), nil
}

func (c *Catalog) addAlias(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	parts := strings.Fields(inv.command.RawArgs)
	if len(parts) != 2 {
		return c.reply(inv, "Usage: <code>addalias alias command</code>"), nil
	}
	target, ok := c.commands.Resolve(parts[1])
	if !ok {
		return c.reply(inv, "Target command was not found."), nil
	}
	if err := c.commands.AddAlias(parts[0], target.Name); err != nil {
		return c.reply(inv, "Alias rejected: <code>"+escape(err.Error())+"</code>"), nil
	}
	aliases := c.loadAliases(ctx)
	aliases[strings.ToLower(parts[0])] = target.Name
	if err := c.saveAliases(ctx, aliases); err != nil {
		return nil, err
	}
	return c.reply(inv, "Alias added."), nil
}

func (c *Catalog) delAlias(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	alias := strings.ToLower(strings.TrimSpace(inv.command.RawArgs))
	aliases := c.loadAliases(ctx)
	if _, ok := aliases[alias]; !ok {
		return c.reply(inv, "Alias was not found."), nil
	}
	delete(aliases, alias)
	c.commands.RemoveAlias(alias)
	if err := c.saveAliases(ctx, aliases); err != nil {
		return nil, err
	}
	return c.reply(inv, "Alias deleted."), nil
}

func (c *Catalog) loadAliases(ctx context.Context) map[string]string {
	aliases := make(map[string]string)
	payload, found, err := c.services.Store.Get(ctx, "core.settings", "aliases")
	if err == nil && found {
		_ = json.Unmarshal(payload, &aliases)
	}
	return aliases
}

func (c *Catalog) saveAliases(ctx context.Context, aliases map[string]string) error {
	payload, err := json.Marshal(aliases)
	if err != nil {
		return err
	}
	return c.services.Store.Set(ctx, "core.settings", "aliases", payload)
}

func (c *Catalog) watchers(_ context.Context, inv invocation) ([]pluginv1.Action, error) {
	return c.reply(inv, "Watchers are filtered in the Go core before bounded plugin admission."), nil
}

func (c *Catalog) restart(ctx context.Context, inv invocation) ([]pluginv1.Action, error) {
	secure := false
	for _, argument := range strings.Fields(inv.command.RawArgs) {
		if argument == "--secure-boot" || argument == "-sb" {
			secure = true
		}
	}
	if c.services.RequestRestart == nil {
		return c.reply(inv, "Restart supervision is not configured."), nil
	}
	if err := c.services.RequestRestart(ctx, secure); err != nil {
		return c.reply(inv, "Restart request failed: <code>"+escape(err.Error())+"</code>"), nil
	}
	if secure {
		return c.reply(inv, "Graceful secure-boot restart requested."), nil
	}
	return c.reply(inv, "Graceful restart requested."), nil
}

func (c *Catalog) source(_ context.Context, inv invocation) ([]pluginv1.Action, error) {
	return c.reply(inv, "<b>Source:</b> "+escape(c.services.SourceURL)), nil
}

func (c *Catalog) ping(_ context.Context, inv invocation) ([]pluginv1.Action, error) {
	uptime := time.Since(c.services.StartedAt).Round(time.Second)
	processed := time.Since(inv.receivedAt)
	if inv.receivedAt.IsZero() {
		processed = 0
	}

	// Telegram message dates are supplied with one-second precision. They are
	// useful for detecting delivery/backlog delay, but are not an RTT measurement.
	updateAge := time.Duration(0)
	if occurred := inv.request.Update.OccurredUnixMS; occurred > 0 {
		updateAge = time.Since(time.UnixMilli(occurred))
		if updateAge < 0 {
			updateAge = 0
		}
	}

	route := "Telegram MTProto → Serenox dispatcher → core.diagnostics → Telegram MTProto"
	return c.reply(inv, fmt.Sprintf(
		"🏓 <b>Pong · Ping diagnostics</b>\n"+
			"⚡ <b>Обработка в ядре:</b> <code>%s</code>\n"+
			"📨 <b>Возраст обновления:</b> <code>≈%s</code> <i>(метка Telegram с точностью до секунды)</i>\n"+
			"🛣 <b>Маршрут:</b> <code>%s</code>\n"+
			"⏱ <b>Uptime:</b> <code>%s</code>\n"+
			"ℹ️ Задержка API Telegram измеряется завершением редактирования этого сообщения; сервер не выдаёт её в самом сообщении.",
		formatLatency(processed), formatLatency(updateAge), route, uptime,
	)), nil
}

func formatLatency(value time.Duration) string {
	if value < time.Millisecond {
		return value.Round(time.Microsecond).String()
	}
	return value.Round(time.Millisecond).String()
}

func (c *Catalog) diagnostics(_ context.Context, inv invocation) ([]pluginv1.Action, error) {
	resources := observability.Resources{}
	if c.services.Resources != nil {
		resources = c.services.Resources()
	}
	rate := ratelimit.Stats{}
	if c.services.RateStats != nil {
		rate = c.services.RateStats()
	}
	queueDepth, active, rejected := 0, int64(0), uint64(0)
	if c.services.ExecutorStats != nil {
		for _, stats := range c.services.ExecutorStats() {
			queueDepth += stats.QueueDepth
			active += stats.Active
			rejected += stats.Rejected
		}
	}
	modulesReady := 0
	if c.services.ModuleStatuses != nil {
		for _, status := range c.services.ModuleStatuses() {
			if status.State == modules.StateReady {
				modulesReady++
			}
		}
	}
	text := fmt.Sprintf(
		"<b>Serenox diagnostics</b>\nGoroutines: <code>%d</code>\nHeap: <code>%d</code>\nRSS: <code>%d</code>\nPeak RSS: <code>%d</code>\nQueue depth: <code>%d</code>\nActive workers: <code>%d</code>\nRejected jobs: <code>%d</code>\nRate identities: <code>%d/%d</code>\nReady modules: <code>%d</code>",
		resources.Goroutines, resources.HeapBytes, resources.RSSBytes, resources.PeakRSS,
		queueDepth, active, rejected, rate.Identities, rate.MaxIdentities, modulesReady,
	)
	if diagnostics, ok := c.services.Plugins.(PluginDiagnostics); ok {
		for _, plugin := range diagnostics.Diagnostics() {
			line := fmt.Sprintf("\nPlugin %s: state=%s process=%s restarts=%d circuit=%s queue=%d workers=%d RSS=%d",
				escape(plugin.ID), escape(plugin.State), escape(plugin.ProcessState), plugin.RestartCount,
				escape(plugin.CircuitState), plugin.QueueDepth, plugin.WorkerCount, plugin.RSSBytes)
			if plugin.Category == "python-sidecar" {
				line += fmt.Sprintf("\nPython: interpreter=%s (%s) sidecar=%s SDK=%s protocol=%s",
					escape(plugin.InterpreterPath), escape(plugin.InterpreterVersion), escape(plugin.SidecarVersion),
					escape(plugin.SDKVersion), escape(plugin.ProtocolVersion))
			}
			if plugin.LastError != "" {
				line += "\nLast error: <code>" + escape(plugin.LastError) + "</code>"
			}
			text += line
		}
	}
	return c.reply(inv, text), nil
}

func (c *Catalog) fail(context.Context, invocation) ([]pluginv1.Action, error) {
	return nil, ErrCommandFailureFixture
}

func renderModuleHelp(definition Definition) string {
	lines := []string{"<b>" + escape(definition.Name) + "</b> — " + escape(definition.Description)}
	for _, command := range definition.Commands {
		line := "• <code>" + command.Name + "</code> — " + escape(command.Description)
		if len(command.Aliases) > 0 {
			line += " (aliases: <code>" + strings.Join(command.Aliases, "</code>, <code>") + "</code>)"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func escape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return strings.ReplaceAll(value, ">", "&gt;")
}
