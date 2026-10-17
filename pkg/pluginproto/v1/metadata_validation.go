package v1

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (m PluginMetadata) Validate() error {
	requiredStrings := []struct {
		field string
		value string
	}{
		{field: "metadata.id", value: m.ID},
		{field: "metadata.name", value: m.Name},
		{field: "metadata.version", value: m.Version},
		{field: "metadata.protocol_version", value: m.ProtocolVersion},
	}
	for _, field := range requiredStrings {
		if err := validString(field.field, field.value, true); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"metadata.description": m.Description,
		"metadata.author":      m.Author,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if m.ProtocolVersion != Version {
		return invalid("metadata.protocol_version", "must be %q", Version)
	}
	if len(m.Commands)+len(m.Watchers)+len(m.InlineHandlers)+len(m.CallbackHandlers)+len(m.PreSendHandlers)+len(m.ControlCommands) > MaxHandlers {
		return invalid("metadata handlers", "exceed limit %d", MaxHandlers)
	}
	if len(m.ControlCommands) > MaxControlCommands {
		return invalid("metadata.control_commands", "exceed limit %d", MaxControlCommands)
	}
	if len(m.Jobs) > MaxJobsPerPlugin {
		return invalid("metadata.jobs", "exceed limit %d", MaxJobsPerPlugin)
	}
	seenJobs := make(map[string]struct{}, len(m.Jobs))
	for index, job := range m.Jobs {
		field := fmt.Sprintf("jobs[%d]", index)
		if err := validIdentifier(field+".name", job.Name); err != nil {
			return err
		}
		if err := validString(field+".description", job.Description, false); err != nil {
			return err
		}
		if job.MaxRuntimeMS < 1 || job.MaxRuntimeMS > 86400000 {
			return invalid(field+".max_runtime_ms", "must be between 1ms and 24h")
		}
		if job.MaxPayloadBytes < 0 || job.MaxPayloadBytes > MaxJobPayloadBytes {
			return invalid(field+".max_payload_bytes", "is outside the bounded range")
		}
		if job.Overlap != JobOverlapSkipIfRunning && job.Overlap != JobOverlapCoalesceOnePending && job.Overlap != JobOverlapQueueOneBounded && job.Overlap != JobOverlapRejectQueueFull {
			return invalid(field+".overlap", "unknown policy")
		}
		if _, exists := seenJobs[job.Name]; exists {
			return invalid("metadata.jobs", "duplicate handler %q", job.Name)
		}
		seenJobs[job.Name] = struct{}{}
	}
	seenControl := make(map[string]struct{}, len(m.ControlCommands))
	for i, command := range m.ControlCommands {
		field := fmt.Sprintf("control_commands[%d]", i)
		if err := validIdentifier(field+".name", command.Name); err != nil {
			return err
		}
		if err := validIdentifier(field+".handler_id", command.HandlerID); err != nil {
			return err
		}
		if err := validString(field+".description", command.Description, false); err != nil {
			return err
		}
		if len(command.Aliases) > MaxControlAliases || len(command.AllowedActorIDs) > MaxControlActorIDs {
			return invalid(field, "collection exceeds control-bot limit")
		}
		key := strings.ToLower(command.Name)
		if _, ok := seenControl[key]; ok {
			return invalid("metadata.control_commands", "duplicate command %q", command.Name)
		}
		seenControl[key] = struct{}{}
		for j, alias := range command.Aliases {
			if err := validIdentifier(fmt.Sprintf("%s.aliases[%d]", field, j), alias); err != nil {
				return err
			}
			alias = strings.ToLower(alias)
			if _, ok := seenControl[alias]; ok {
				return invalid("metadata.control_commands", "duplicate command or alias %q", alias)
			}
			seenControl[alias] = struct{}{}
		}
		if len(command.Surfaces) == 0 || len(command.Surfaces) > 2 {
			return invalid(field+".surfaces", "must contain one or two surfaces")
		}
		surfaces := map[ControlSurface]struct{}{}
		for _, surface := range command.Surfaces {
			if surface != ControlSurfaceInline && surface != ControlSurfacePrivate {
				return invalid(field+".surfaces", "unknown surface %q", surface)
			}
			if _, ok := surfaces[surface]; ok {
				return invalid(field+".surfaces", "duplicate surface %q", surface)
			}
			surfaces[surface] = struct{}{}
		}
		if command.ActorPolicy == "" {
			command.ActorPolicy = ControlActorOwnerOnly
		}
		if command.ActorPolicy != ControlActorOwnerOnly && command.ActorPolicy != ControlActorAllowList {
			return invalid(field+".actor_policy", "unknown policy %q", command.ActorPolicy)
		}
		if command.ActorPolicy == ControlActorAllowList && len(command.AllowedActorIDs) == 0 {
			return invalid(field+".allowed_actor_ids", "is required for allow_list")
		}
		if err := uniqueInt64s(field+".allowed_actor_ids", command.AllowedActorIDs); err != nil {
			return err
		}
		if command.ConfirmationTimeoutMS < 0 || command.ConfirmationTimeoutMS > 24*60*60*1000 {
			return invalid(field+".confirmation_timeout_ms", "is outside the bounded range")
		}
		if command.RequireConfirmation && command.ConfirmationTimeoutMS == 0 {
			return invalid(field+".confirmation_timeout_ms", "is required when confirmation is required")
		}
		if command.Mutating && command.AllowQueryExecution {
			return invalid(field+".allow_query_execution", "mutating commands cannot execute while an inline query is typed")
		}
		if command.MaxArgumentBytes < 0 || command.MaxArgumentBytes > MaxControlQueryBytes || command.MaxStateBytes < 0 || command.MaxStateBytes > MaxControlStateBytes {
			return invalid(field, "argument or state limit is outside bounds")
		}
		if command.ResponseMode != ControlResponseInlineResult && command.ResponseMode != ControlResponseBotMessage && command.ResponseMode != ControlResponseCallbackFlow {
			return invalid(field+".response_mode", "unknown response mode %q", command.ResponseMode)
		}
	}
	if len(m.Config) > MaxCollectionItems || len(m.Capabilities) > MaxCollectionItems {
		return invalid("metadata", "collection exceeds limit %d", MaxCollectionItems)
	}
	seenCapabilities := make(map[Capability]struct{}, len(m.Capabilities))
	for index, capability := range m.Capabilities {
		if !knownCapability(capability) {
			return invalid(fmt.Sprintf("metadata.capabilities[%d]", index), "unknown capability %q", capability)
		}
		if _, exists := seenCapabilities[capability]; exists {
			return invalid("metadata.capabilities", "duplicate capability %q", capability)
		}
		seenCapabilities[capability] = struct{}{}
	}
	if len(m.EventPublications) > MaxEventPublications || len(m.EventSubscriptions) > MaxEventSubscriptions {
		return invalid("metadata.events", "declarations exceed bounded limits")
	}
	publications := make(map[string]struct{}, len(m.EventPublications))
	for i, declaration := range m.EventPublications {
		if err := declaration.Validate(); err != nil {
			return invalid(fmt.Sprintf("event_publications[%d]", i), "%v", err)
		}
		key := fmt.Sprintf("%s/v%d", declaration.Type, declaration.Version)
		if _, ok := publications[key]; ok {
			return invalid("metadata.event_publications", "duplicate declaration %q", key)
		}
		publications[key] = struct{}{}
	}
	subscriptions := make(map[string]struct{}, len(m.EventSubscriptions))
	for i, declaration := range m.EventSubscriptions {
		if err := declaration.Validate(); err != nil {
			return invalid(fmt.Sprintf("event_subscriptions[%d]", i), "%v", err)
		}
		if _, ok := subscriptions[declaration.HandlerID]; ok {
			return invalid("metadata.event_subscriptions", "duplicate handler %q", declaration.HandlerID)
		}
		subscriptions[declaration.HandlerID] = struct{}{}
	}
	if len(m.EventPublications) > 0 {
		if _, ok := seenCapabilities[CapabilityEventsPublish]; !ok {
			return invalid("metadata.capabilities", "event publications require events.publish")
		}
	}
	if len(m.EventSubscriptions) > 0 {
		if _, ok := seenCapabilities[CapabilityEventsSubscribe]; !ok {
			return invalid("metadata.capabilities", "event subscriptions require events.subscribe")
		}
	}

	seenCommands := make(map[string]struct{}, len(m.Commands))
	totalAliases := 0
	for i, command := range m.Commands {
		if err := validIdentifier(fmt.Sprintf("commands[%d].name", i), command.Name); err != nil {
			return err
		}
		if err := validString(fmt.Sprintf("commands[%d].description", i), command.Description, false); err != nil {
			return err
		}
		commandKey := strings.ToLower(command.Name)
		if _, ok := seenCommands[commandKey]; ok {
			return invalid("metadata.commands", "duplicate command %q", command.Name)
		}
		seenCommands[commandKey] = struct{}{}
		if len(command.Aliases) > MaxCollectionItems {
			return invalid("metadata.commands.aliases", "has too many entries")
		}
		if totalAliases > MaxCollectionItems-len(command.Aliases) {
			return invalid("metadata.commands.aliases", "total exceeds limit %d", MaxCollectionItems)
		}
		totalAliases += len(command.Aliases)
		for aliasIndex, alias := range command.Aliases {
			if err := validIdentifier(fmt.Sprintf("commands[%d].aliases[%d]", i, aliasIndex), alias); err != nil {
				return err
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := seenCommands[aliasKey]; exists {
				return invalid("metadata.commands", "duplicate command or alias %q", alias)
			}
			seenCommands[aliasKey] = struct{}{}
		}
	}
	seenWatchers := make(map[string]struct{}, len(m.Watchers))
	for i, watcher := range m.Watchers {
		if err := validIdentifier(fmt.Sprintf("watchers[%d].id", i), watcher.ID); err != nil {
			return err
		}
		if err := validString(fmt.Sprintf("watchers[%d].description", i), watcher.Description, false); err != nil {
			return err
		}
		if _, ok := seenWatchers[watcher.ID]; ok {
			return invalid("metadata.watchers", "duplicate watcher %q", watcher.ID)
		}
		seenWatchers[watcher.ID] = struct{}{}
		if err := watcher.Filter.Validate(); err != nil {
			return invalid(fmt.Sprintf("watchers[%d].filter", i), "%v", err)
		}
	}
	seenInline := make(map[string]struct{}, len(m.InlineHandlers))
	for i, spec := range m.InlineHandlers {
		field := fmt.Sprintf("inline_handlers[%d]", i)
		if err := validIdentifier(field+".name", spec.Name); err != nil {
			return err
		}
		if err := validString(field+".description", spec.Description, false); err != nil {
			return err
		}
		if err := validString(field+".query_prefix", spec.QueryPrefix, false); err != nil {
			return err
		}
		if len(spec.SenderIDs) > MaxCollectionItems || len(spec.AllowedUsers) > MaxCollectionItems {
			return invalid(field, "ID collection exceeds limit %d", MaxCollectionItems)
		}
		if err := uniqueInt64s(field+".sender_ids", spec.SenderIDs); err != nil {
			return err
		}
		if err := uniqueInt64s(field+".allowed_users", spec.AllowedUsers); err != nil {
			return err
		}
		if _, exists := seenInline[spec.Name]; exists {
			return invalid("metadata.inline_handlers", "duplicate handler %q", spec.Name)
		}
		seenInline[spec.Name] = struct{}{}
	}
	seenCallbacks := make(map[string]struct{}, len(m.CallbackHandlers))
	for i, spec := range m.CallbackHandlers {
		field := fmt.Sprintf("callback_handlers[%d]", i)
		if err := validIdentifier(field+".name", spec.Name); err != nil {
			return err
		}
		if err := validString(field+".description", spec.Description, false); err != nil {
			return err
		}
		if err := validString(field+".data", spec.Data, false); err != nil {
			return err
		}
		if err := validString(field+".data_prefix", spec.DataPrefix, false); err != nil {
			return err
		}
		if len(spec.Data) > MaxCallbackDataBytes || len(spec.DataPrefix) > MaxCallbackDataBytes {
			return invalid(field, "callback data exceeds %d bytes", MaxCallbackDataBytes)
		}
		if spec.TTLMS < 0 || spec.TTLMS > 24*60*60*1000 {
			return invalid(field+".ttl_ms", "must be between 0 and 86400000")
		}
		if len(spec.ChatIDs) > MaxCollectionItems || len(spec.SenderIDs) > MaxCollectionItems || len(spec.AllowedUsers) > MaxCollectionItems {
			return invalid(field, "ID collection exceeds limit %d", MaxCollectionItems)
		}
		if err := uniqueInt64s(field+".chat_ids", spec.ChatIDs); err != nil {
			return err
		}
		if err := uniqueInt64s(field+".sender_ids", spec.SenderIDs); err != nil {
			return err
		}
		if err := uniqueInt64s(field+".allowed_users", spec.AllowedUsers); err != nil {
			return err
		}
		if _, exists := seenCallbacks[spec.Name]; exists {
			return invalid("metadata.callback_handlers", "duplicate handler %q", spec.Name)
		}
		seenCallbacks[spec.Name] = struct{}{}
	}
	seenPreSend := make(map[string]struct{}, len(m.PreSendHandlers))
	for i, spec := range m.PreSendHandlers {
		field := fmt.Sprintf("pre_send_handlers[%d]", i)
		if err := validIdentifier(field+".name", spec.Name); err != nil {
			return err
		}
		if err := validString(field+".description", spec.Description, false); err != nil {
			return err
		}
		if _, exists := seenPreSend[spec.Name]; exists {
			return invalid("metadata.pre_send_handlers", "duplicate handler %q", spec.Name)
		}
		seenPreSend[spec.Name] = struct{}{}
	}
	seenConfig := make(map[string]struct{}, len(m.Config))
	for index, spec := range m.Config {
		if err := validIdentifier(fmt.Sprintf("config[%d].key", index), spec.Key); err != nil {
			return err
		}
		if !knownConfigType(spec.Type) {
			return invalid(fmt.Sprintf("config[%d].type", index), "unknown config type %q", spec.Type)
		}
		if err := validString(fmt.Sprintf("config[%d].description", index), spec.Description, false); err != nil {
			return err
		}
		if _, exists := seenConfig[spec.Key]; exists {
			return invalid("metadata.config", "duplicate key %q", spec.Key)
		}
		seenConfig[spec.Key] = struct{}{}
		if len(spec.Default) > MaxStringBytes {
			return invalid(fmt.Sprintf("config[%d].default", index), "exceeds %d bytes", MaxStringBytes)
		}
		if len(spec.Default) != 0 && !json.Valid(spec.Default) {
			return invalid(fmt.Sprintf("config[%d].default", index), "must be one JSON value")
		}
	}
	return nil
}

func (f WatcherFilter) Validate() error {
	if len(f.Kinds) > MaxCollectionItems || len(f.ChatIDs) > MaxCollectionItems || len(f.SenderIDs) > MaxCollectionItems {
		return invalid("watcher.filter", "collection exceeds limit %d", MaxCollectionItems)
	}
	if err := validString("watcher.filter.text_prefix", f.TextPrefix, false); err != nil {
		return err
	}
	if len(f.TextPrefix) > MaxWatcherTextBytes {
		return invalid("watcher.filter.text_prefix", "exceeds %d bytes", MaxWatcherTextBytes)
	}
	seenKinds := make(map[UpdateKind]struct{}, len(f.Kinds))
	for index, kind := range f.Kinds {
		if !knownUpdateKind(kind) {
			return invalid(fmt.Sprintf("watcher.filter.kinds[%d]", index), "unknown update kind %q", kind)
		}
		if _, exists := seenKinds[kind]; exists {
			return invalid("watcher.filter.kinds", "duplicate update kind %q", kind)
		}
		seenKinds[kind] = struct{}{}
	}
	if err := uniqueInt64s("watcher.filter.chat_ids", f.ChatIDs); err != nil {
		return err
	}
	return uniqueInt64s("watcher.filter.sender_ids", f.SenderIDs)
}
