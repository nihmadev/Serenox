package v1

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func knownCapability(capability Capability) bool {
	switch capability {
	case CapabilityCommands, CapabilityWatchers, CapabilityInline, CapabilityStorage, CapabilityClient,
		CapabilityClientIdentity, CapabilityClientPeer, CapabilityClientUser, CapabilityClientChat,
		CapabilityClientMessage, CapabilityClientHistory, CapabilityClientSearch, CapabilityClientDialogs,
		CapabilityClientParticipants, CapabilityClientMediaMetadata, CapabilityMediaRead, CapabilityMediaWrite,
		CapabilityMediaAttach, CapabilityHTTP, CapabilityPreSend, CapabilityControlBot, CapabilityControlBotInline,
		CapabilityControlBotCallbacks, CapabilityJobs, CapabilityModeration, CapabilityProfile, CapabilityEventsPublish, CapabilityEventsSubscribe:
		return true
	default:
		return false
	}
}

func knownUpdateKind(kind UpdateKind) bool {
	switch kind {
	case UpdateMessage, UpdateEditedMessage, UpdateDeleted, UpdateCallback, UpdateInlineQuery, UpdateControlMessage, UpdateControlCallback, UpdateControlInlineQuery:
		return true
	default:
		return false
	}
}

func knownConfigType(configType ConfigType) bool {
	switch configType {
	case ConfigString, ConfigInteger, ConfigNumber, ConfigBoolean, ConfigStrings, ConfigJSON:
		return true
	default:
		return false
	}
}

func uniqueInt64s(field string, values []int64) error {
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return invalid(field, "duplicate value %d", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidDTO, field, fmt.Sprintf(format, args...))
}

func validString(field, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalid(field, "is required")
	}
	if !utf8.ValidString(value) {
		return invalid(field, "must be valid UTF-8")
	}
	if len(value) > MaxStringBytes {
		return invalid(field, "exceeds %d bytes", MaxStringBytes)
	}
	return nil
}

func validIdentifier(field, value string) error {
	if err := validString(field, value, true); err != nil {
		return err
	}
	if len(value) > 128 {
		return invalid(field, "exceeds 128 bytes")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return invalid(field, "contains unsupported character %q", r)
	}
	return nil
}
