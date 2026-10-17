package plugin

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func validateHandshake(response protocol.HandshakeResponse, nonce string, requestedMax uint32) error {
	if err := response.Validate(); err != nil {
		return err
	}
	if response.Nonce != nonce {
		return fmt.Errorf("%w: handshake nonce did not match", protocol.ErrInvalidDTO)
	}
	if response.MaxFrameBytes > requestedMax {
		return fmt.Errorf("%w: peer frame maximum %d exceeds offered %d", protocol.ErrInvalidDTO, response.MaxFrameBytes, requestedMax)
	}
	return nil
}

func validateLifecycle(response protocol.LifecycleResponse, expected string) error {
	if response.State != expected {
		return fmt.Errorf("%w: lifecycle state %q, expected %q", protocol.ErrInvalidDTO, response.State, expected)
	}
	if len(response.Actions) > protocol.MaxActions {
		return fmt.Errorf("%w: lifecycle actions exceed %d", protocol.ErrInvalidDTO, protocol.MaxActions)
	}
	for index := range response.Actions {
		if err := response.Actions[index].Validate(); err != nil {
			return fmt.Errorf("%w: lifecycle action %d: %v", protocol.ErrInvalidDTO, index, err)
		}
	}
	return nil
}

func cloneMetadata(metadata protocol.PluginMetadata) protocol.PluginMetadata {
	copy := metadata
	copy.Capabilities = append([]protocol.Capability(nil), metadata.Capabilities...)
	copy.Commands = append([]protocol.CommandSpec(nil), metadata.Commands...)
	for index := range copy.Commands {
		copy.Commands[index].Aliases = append([]string(nil), metadata.Commands[index].Aliases...)
	}
	copy.Watchers = append([]protocol.WatcherSpec(nil), metadata.Watchers...)
	for index := range copy.Watchers {
		filter := metadata.Watchers[index].Filter
		filter.Kinds = append([]protocol.UpdateKind(nil), filter.Kinds...)
		filter.ChatIDs = append([]int64(nil), filter.ChatIDs...)
		filter.SenderIDs = append([]int64(nil), filter.SenderIDs...)
		copy.Watchers[index].Filter = filter
	}
	copy.InlineHandlers = append([]protocol.InlineSpec(nil), metadata.InlineHandlers...)
	for index := range copy.InlineHandlers {
		copy.InlineHandlers[index].SenderIDs = append([]int64(nil), metadata.InlineHandlers[index].SenderIDs...)
		copy.InlineHandlers[index].AllowedUsers = append([]int64(nil), metadata.InlineHandlers[index].AllowedUsers...)
	}
	copy.CallbackHandlers = append([]protocol.CallbackSpec(nil), metadata.CallbackHandlers...)
	for index := range copy.CallbackHandlers {
		copy.CallbackHandlers[index].ChatIDs = append([]int64(nil), metadata.CallbackHandlers[index].ChatIDs...)
		copy.CallbackHandlers[index].SenderIDs = append([]int64(nil), metadata.CallbackHandlers[index].SenderIDs...)
		copy.CallbackHandlers[index].AllowedUsers = append([]int64(nil), metadata.CallbackHandlers[index].AllowedUsers...)
	}
	copy.PreSendHandlers = append([]protocol.PreSendSpec(nil), metadata.PreSendHandlers...)
	copy.ControlCommands = append([]protocol.ControlCommand(nil), metadata.ControlCommands...)
	copy.Jobs = append([]protocol.JobHandlerSpec(nil), metadata.Jobs...)
	copy.EventPublications = append([]protocol.EventPublication(nil), metadata.EventPublications...)
	copy.EventSubscriptions = append([]protocol.EventSubscription(nil), metadata.EventSubscriptions...)
	for index := range copy.ControlCommands {
		copy.ControlCommands[index].Aliases = append([]string(nil), metadata.ControlCommands[index].Aliases...)
		copy.ControlCommands[index].Surfaces = append([]protocol.ControlSurface(nil), metadata.ControlCommands[index].Surfaces...)
		copy.ControlCommands[index].AllowedActorIDs = append([]int64(nil), metadata.ControlCommands[index].AllowedActorIDs...)
	}
	copy.Config = append([]protocol.ConfigSpec(nil), metadata.Config...)
	for index := range copy.Config {
		copy.Config[index].Default = append([]byte(nil), metadata.Config[index].Default...)
	}
	return copy
}

func cloneConfigValues(values []protocol.ConfigValue) []protocol.ConfigValue {
	result := append([]protocol.ConfigValue(nil), values...)
	for index := range result {
		result[index].Value = append([]byte(nil), values[index].Value...)
	}
	return result
}

func childEnvironment(extra []string, injected map[string]string) []string {
	blocked := make(map[string]struct{}, len(injected))
	for key := range injected {
		blocked[key] = struct{}{}
	}
	// A plugin receives a deliberately tiny ambient environment. Credentials,
	// proxy settings, cloud tokens, and arbitrary host configuration are never
	// inherited; callers must opt in through SupervisorConfig.Env.
	base := make([]string, 0, len(extra)+2)
	for _, key := range []string{"PATH", "SystemRoot"} {
		if value, ok := os.LookupEnv(key); ok {
			base = append(base, key+"="+value)
		}
	}
	base = append(base, extra...)
	result := make([]string, 0, len(base)+len(injected))
	positions := make(map[string]int, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, omit := blocked[key]; omit {
			continue
		}
		if position, exists := positions[key]; exists {
			result[position] = entry
			continue
		}
		positions[key] = len(result)
		result = append(result, entry)
	}
	for _, key := range []string{envSocket, envToken, envInstanceID, envProtocol, envMaxFrameSize} {
		result = append(result, key+"="+injected[key])
	}
	return result
}

func randomHex(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
