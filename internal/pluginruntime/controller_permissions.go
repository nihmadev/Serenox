package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func clonePluginMetadata(metadata pluginv1.PluginMetadata) pluginv1.PluginMetadata {
	result := metadata
	result.Capabilities = append([]pluginv1.Capability(nil), metadata.Capabilities...)
	result.Commands = append([]pluginv1.CommandSpec(nil), metadata.Commands...)
	for index := range result.Commands {
		result.Commands[index].Aliases = append([]string(nil), metadata.Commands[index].Aliases...)
	}
	result.Watchers = append([]pluginv1.WatcherSpec(nil), metadata.Watchers...)
	for index := range result.Watchers {
		result.Watchers[index].Filter.Kinds = append([]pluginv1.UpdateKind(nil), metadata.Watchers[index].Filter.Kinds...)
		result.Watchers[index].Filter.ChatIDs = append([]int64(nil), metadata.Watchers[index].Filter.ChatIDs...)
		result.Watchers[index].Filter.SenderIDs = append([]int64(nil), metadata.Watchers[index].Filter.SenderIDs...)
	}
	result.InlineHandlers = append([]pluginv1.InlineSpec(nil), metadata.InlineHandlers...)
	for index := range result.InlineHandlers {
		result.InlineHandlers[index].SenderIDs = append([]int64(nil), metadata.InlineHandlers[index].SenderIDs...)
		result.InlineHandlers[index].AllowedUsers = append([]int64(nil), metadata.InlineHandlers[index].AllowedUsers...)
	}
	result.CallbackHandlers = append([]pluginv1.CallbackSpec(nil), metadata.CallbackHandlers...)
	for index := range result.CallbackHandlers {
		result.CallbackHandlers[index].ChatIDs = append([]int64(nil), metadata.CallbackHandlers[index].ChatIDs...)
		result.CallbackHandlers[index].SenderIDs = append([]int64(nil), metadata.CallbackHandlers[index].SenderIDs...)
		result.CallbackHandlers[index].AllowedUsers = append([]int64(nil), metadata.CallbackHandlers[index].AllowedUsers...)
	}
	result.PreSendHandlers = append([]pluginv1.PreSendSpec(nil), metadata.PreSendHandlers...)
	result.Jobs = append([]pluginv1.JobHandlerSpec(nil), metadata.Jobs...)
	result.EventPublications = append([]pluginv1.EventPublication(nil), metadata.EventPublications...)
	result.EventSubscriptions = append([]pluginv1.EventSubscription(nil), metadata.EventSubscriptions...)
	result.ControlCommands = append([]pluginv1.ControlCommand(nil), metadata.ControlCommands...)
	for index := range result.ControlCommands {
		result.ControlCommands[index].Aliases = append([]string(nil), metadata.ControlCommands[index].Aliases...)
		result.ControlCommands[index].Surfaces = append([]pluginv1.ControlSurface(nil), metadata.ControlCommands[index].Surfaces...)
		result.ControlCommands[index].AllowedActorIDs = append([]int64(nil), metadata.ControlCommands[index].AllowedActorIDs...)
	}
	result.Config = append([]pluginv1.ConfigSpec(nil), metadata.Config...)
	for index := range result.Config {
		result.Config[index].Default = append([]byte(nil), metadata.Config[index].Default...)
	}
	return result
}

func permissionCheckedDispatch(process Process, declared []string) DispatchFunc {
	permissions := make(map[string]struct{}, len(declared))
	for _, permission := range declared {
		permissions[permission] = struct{}{}
	}
	return func(ctx context.Context, request pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
		response, err := process.Dispatch(ctx, request)
		if err != nil {
			return pluginv1.DispatchResponse{}, err
		}
		response = cloneDispatchResponse(response)
		if err := response.Validate(); err != nil {
			return pluginv1.DispatchResponse{}, fmt.Errorf("validate plugin dispatch response: %w", err)
		}
		if err := checkActionPermissions(response.Actions, permissions); err != nil {
			return pluginv1.DispatchResponse{}, err
		}
		if request.PreSend != nil {
			if response.PreSend == nil {
				return pluginv1.DispatchResponse{}, fmt.Errorf("%w: pre-send response is missing", pluginv1.ErrInvalidDTO)
			}
			if err := checkPreSendRewrite(request.PreSend.Action, response.PreSend.Action); err != nil {
				return pluginv1.DispatchResponse{}, err
			}
		}
		return response, nil
	}
}

func checkPreSendRewrite(original, transformed pluginv1.Action) error {
	if transformed.ID != original.ID || transformed.Kind != pluginv1.ActionSend || transformed.ChatID != original.ChatID ||
		transformed.ReplyToMessageID != original.ReplyToMessageID || transformed.IdempotencyKey != original.IdempotencyKey {
		return fmt.Errorf("%w: pre-send hook changed immutable send fields", pluginv1.ErrInvalidDTO)
	}
	return nil
}

func checkActionPermissions(actions []pluginv1.Action, permissions map[string]struct{}) error {
	for index, action := range actions {
		required := actionPermission(action)
		if _, allowed := permissions[required]; !allowed {
			return fmt.Errorf(
				"%w: action %d of kind %q requires %q", ErrPermissionDenied, index, action.Kind, required,
			)
		}
	}
	return nil
}

func permissionCheckedActions(
	apply func(context.Context, []pluginv1.Action) error, declared []string,
) func(context.Context, []pluginv1.Action) error {
	permissions := make(map[string]struct{}, len(declared))
	for _, permission := range declared {
		permissions[permission] = struct{}{}
	}
	return func(ctx context.Context, actions []pluginv1.Action) error {
		if err := checkActionPermissions(actions, permissions); err != nil {
			return err
		}
		if apply == nil {
			return errors.New("reverse action handler is not configured")
		}
		return apply(ctx, actions)
	}
}

func permissionCheckedActionResults(
	apply func(context.Context, []pluginv1.Action) (pluginv1.ActionBatchResponse, error), declared []string,
) func(context.Context, []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
	permissions := make(map[string]struct{}, len(declared))
	for _, permission := range declared {
		permissions[permission] = struct{}{}
	}
	return func(ctx context.Context, actions []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
		if err := checkActionPermissions(actions, permissions); err != nil {
			return pluginv1.ActionBatchResponse{}, err
		}
		if apply == nil {
			return pluginv1.ActionBatchResponse{}, errors.New("reverse action result handler is not configured")
		}
		return apply(ctx, actions)
	}
}

func actionPermission(action pluginv1.Action) string {
	switch action.Kind {
	case pluginv1.ActionReply:
		if action.Media != nil {
			return "media.attach"
		}
		return "telegram.reply"
	case pluginv1.ActionEdit:
		if action.Media != nil {
			return "media.attach"
		}
		return "telegram.edit"
	case pluginv1.ActionDelete:
		return "telegram.delete"
	case pluginv1.ActionSend:
		if action.Media != nil {
			return "media.attach"
		}
		return "telegram.send"
	case pluginv1.ActionInlineAnswer:
		return "telegram.inline"
	case pluginv1.ActionBotAnswerInline:
		return "telegram.bot.write_ui"
	case pluginv1.ActionBotAnswerCallback:
		return "telegram.bot.write_ui"
	case pluginv1.ActionBotSend, pluginv1.ActionBotEdit, pluginv1.ActionBotDelete:
		return "telegram.bot.write_ui"
	case pluginv1.ActionProfileUpdateName:
		return pluginv1.ProfilePermission
	case pluginv1.ActionClient:
		if action.ClientOperation != nil {
			if action.ClientOperation.Name == "storage.get" || action.ClientOperation.Name == "storage.list" {
				return "storage.read"
			}
			if action.ClientOperation.Name == "storage.set" || action.ClientOperation.Name == "storage.delete" || action.ClientOperation.Name == "storage.batch" {
				return "storage.write"
			}
			return "client." + action.ClientOperation.Name
		}
	}
	return ""
}

func permissionCheckedMedia(
	handle func(context.Context, string, int64, pluginv1.MediaRequest) (pluginv1.MediaResult, error), declared []string,
) func(context.Context, string, int64, pluginv1.MediaRequest) (pluginv1.MediaResult, error) {
	permissions := permissionSet(declared)
	return func(ctx context.Context, pluginID string, accountID int64, request pluginv1.MediaRequest) (pluginv1.MediaResult, error) {
		required := "media.read"
		switch request.Operation {
		case pluginv1.MediaUpload:
			required = "media.write"
		case pluginv1.MediaAttach:
			required = "media.attach"
		case pluginv1.MediaDelete:
			required = "media.write"
		}
		if _, ok := permissions[required]; !ok {
			return pluginv1.MediaResult{}, fmt.Errorf("%w: media operation %q requires %q", ErrPermissionDenied, request.Operation, required)
		}
		if handle == nil {
			return pluginv1.MediaResult{}, errors.New("media handler is not configured")
		}
		return handle(ctx, pluginID, accountID, request)
	}
}

func permissionSet(declared []string) map[string]struct{} {
	result := make(map[string]struct{}, len(declared))
	for _, permission := range declared {
		result[permission] = struct{}{}
	}
	return result
}

func permissionCheckedJobs(handle func(context.Context, string, int64, pluginv1.JobRPCRequest) (any, error), declared []string, capabilities []string) func(context.Context, string, int64, pluginv1.JobRPCRequest) (any, error) {
	permissions := permissionSet(declared)
	capable := false
	for _, capability := range capabilities {
		if capability == string(pluginv1.CapabilityJobs) {
			capable = true
			break
		}
	}
	return func(ctx context.Context, pluginID string, accountID int64, request pluginv1.JobRPCRequest) (any, error) {
		if !capable {
			return nil, fmt.Errorf("%w: jobs capability is not declared", ErrPermissionDenied)
		}
		if _, ok := permissions["jobs.schedule"]; !ok {
			return nil, fmt.Errorf("%w: jobs.schedule permission is required", ErrPermissionDenied)
		}
		if handle == nil {
			return nil, errors.New("jobs handler is not configured")
		}
		return handle(ctx, pluginID, accountID, request)
	}
}

func permissionCheckedStorage(
	handle func(context.Context, string, int64, pluginv1.StorageRequest) (pluginv1.StorageResult, error), declared []string,
) func(context.Context, string, int64, pluginv1.StorageRequest) (pluginv1.StorageResult, error) {
	permissions := permissionSet(declared)
	return func(ctx context.Context, pluginID string, accountID int64, request pluginv1.StorageRequest) (pluginv1.StorageResult, error) {
		required := "storage.read"
		if request.Operation == pluginv1.StorageSet || request.Operation == pluginv1.StorageDelete || request.Operation == pluginv1.StorageBatch {
			required = "storage.write"
		}
		if _, ok := permissions[required]; !ok {
			return pluginv1.StorageResult{}, fmt.Errorf("%w: storage operation %q requires %q", ErrPermissionDenied, request.Operation, required)
		}
		if handle == nil {
			return pluginv1.StorageResult{}, errors.New("storage handler is not configured")
		}
		return handle(ctx, pluginID, accountID, request)
	}
}

func permissionCheckedClient(
	handle func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error), declared []string,
) func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	permissions := permissionSet(declared)
	return func(ctx context.Context, pluginID string, accountID int64, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
		required := pluginv1.ClientPermission(request.Operation)
		legacy := "client." + string(request.Operation)
		if required == "" {
			return pluginv1.ClientResult{}, fmt.Errorf("%w: client operation %q is unknown", ErrPermissionDenied, request.Operation)
		}
		if _, ok := permissions[required]; !ok {
			if _, legacyOK := permissions[legacy]; legacyOK {
			} else {
				return pluginv1.ClientResult{}, fmt.Errorf("%w: client operation %q requires %q", ErrPermissionDenied, request.Operation, required)
			}
		}
		if handle == nil {
			return pluginv1.ClientResult{}, errors.New("client handler is not configured")
		}
		return handle(ctx, pluginID, accountID, request)
	}
}

func permissionCheckedModeration(
	handle func(context.Context, string, int64, string, pluginv1.ModerationRequest) (pluginv1.ModerationResult, error), declaredPermissions, declaredCapabilities []string,
) func(context.Context, string, int64, string, pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
	permissions := permissionSet(declaredPermissions)
	capable := false
	for _, capability := range declaredCapabilities {
		if capability == string(pluginv1.CapabilityModeration) {
			capable = true
			break
		}
	}
	return func(ctx context.Context, pluginID string, accountID int64, _ string, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
		if !capable {
			return pluginv1.ModerationResult{}, fmt.Errorf("%w: moderation capability is not declared", ErrPermissionDenied)
		}
		required := pluginv1.ModerationPermission(request.Operation)
		if required == "" {
			return pluginv1.ModerationResult{}, fmt.Errorf("%w: moderation operation %q is unknown", ErrPermissionDenied, request.Operation)
		}
		if _, ok := permissions[required]; !ok {
			return pluginv1.ModerationResult{}, fmt.Errorf("%w: moderation operation %q requires %q", ErrPermissionDenied, request.Operation, required)
		}
		if handle == nil {
			return pluginv1.ModerationResult{}, errors.New("moderation handler is not configured")
		}
		return handle(ctx, pluginID, accountID, required, request)
	}
}

func permissionCheckedHTTP(
	handle func(context.Context, string, int64, pluginpackage.NetworkPolicy, pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error),
	declared []string, policy pluginpackage.NetworkPolicy, capabilities []string,
) func(context.Context, string, int64, pluginpackage.NetworkPolicy, pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error) {
	permissions := permissionSet(declared)
	capable := false
	for _, capability := range capabilities {
		if capability == "http" {
			capable = true
			break
		}
	}
	return func(ctx context.Context, pluginID string, accountID int64, _ pluginpackage.NetworkPolicy, request pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error) {
		if !capable {
			return pluginv1.HTTPResponse{}, fmt.Errorf("%w: HTTP capability is not declared", ErrPermissionDenied)
		}
		required := "network.http.write"
		if request.Method == "GET" || request.Method == "HEAD" {
			required = "network.http.read"
		}
		if _, ok := permissions[required]; !ok {
			return pluginv1.HTTPResponse{}, fmt.Errorf("%w: HTTP method %q requires %q", ErrPermissionDenied, request.Method, required)
		}
		if request.Method != "GET" && request.Method != "HEAD" && request.Method != "POST" && request.Method != "PUT" && request.Method != "PATCH" && request.Method != "DELETE" {
			return pluginv1.HTTPResponse{}, fmt.Errorf("%w: unsupported HTTP method", ErrPermissionDenied)
		}
		if handle == nil {
			return pluginv1.HTTPResponse{}, errors.New("HTTP handler is not configured")
		}
		return handle(ctx, pluginID, accountID, policy, request)
	}
}

func cloneDispatchResponse(response pluginv1.DispatchResponse) pluginv1.DispatchResponse {
	result := response
	result.Actions = append([]pluginv1.Action(nil), response.Actions...)
	for index := range result.Actions {
		result.Actions[index].Buttons = cloneInlineButtons(response.Actions[index].Buttons)
		result.Actions[index].InlineResults = append([]pluginv1.InlineResult(nil), response.Actions[index].InlineResults...)
		for resultIndex := range result.Actions[index].InlineResults {
			result.Actions[index].InlineResults[resultIndex].Buttons = cloneInlineButtons(
				response.Actions[index].InlineResults[resultIndex].Buttons,
			)
		}
		if response.Actions[index].ClientOperation != nil {
			operation := *response.Actions[index].ClientOperation
			result.Actions[index].ClientOperation = &operation
		}
		if response.Actions[index].Media != nil {
			media := *response.Actions[index].Media
			if media.Attributes != nil {
				attributes := *media.Attributes
				media.Attributes = &attributes
			}
			result.Actions[index].Media = &media
		}
	}
	return result
}

func cloneInlineButtons(buttons [][]pluginv1.InlineButton) [][]pluginv1.InlineButton {
	result := make([][]pluginv1.InlineButton, len(buttons))
	for index := range buttons {
		result[index] = append([]pluginv1.InlineButton(nil), buttons[index]...)
	}
	return result
}

func capabilitiesMatch(declared []string, reported []pluginv1.Capability) bool {
	left := append([]string(nil), declared...)
	right := make([]string, len(reported))
	for index, capability := range reported {
		right[index] = string(capability)
	}
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
