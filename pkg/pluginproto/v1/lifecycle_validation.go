package v1

import (
	"encoding/json"
	"fmt"
)

func (h HandshakeRequest) Validate() error {
	if len(h.SupportedVersions) == 0 || len(h.SupportedVersions) > 16 {
		return invalid("supported_versions", "must contain 1..16 versions")
	}
	if len(h.Features) > MaxCollectionItems {
		return invalid("features", "exceed limit %d", MaxCollectionItems)
	}
	for index, feature := range h.Features {
		if err := validString(fmt.Sprintf("features[%d]", index), feature, true); err != nil {
			return err
		}
	}
	if err := validString("auth_token", h.AuthToken, true); err != nil {
		return err
	}
	if err := validIdentifier("instance_id", h.InstanceID); err != nil {
		return err
	}
	if err := validString("nonce", h.Nonce, true); err != nil {
		return err
	}
	if h.MaxFrameBytes == 0 {
		return invalid("max_frame_bytes", "must be positive")
	}
	return nil
}

func (h HandshakeResponse) Validate() error {
	if h.SelectedVersion != Version {
		return invalid("selected_version", "must be %q", Version)
	}
	if err := validString("nonce", h.Nonce, true); err != nil {
		return err
	}
	if h.MaxFrameBytes == 0 {
		return invalid("max_frame_bytes", "must be positive")
	}
	return h.Metadata.Validate()
}

func (d DispatchRequest) Validate() error {
	if d.Event != nil {
		if d.Update.ID != "" || d.Command != nil || d.PreSend != nil || d.Job != nil {
			return invalid("dispatch", "event delivery cannot be combined with another dispatch kind")
		}
		if err := validIdentifier("event.subscription_id", d.Event.SubscriptionID); err != nil {
			return err
		}
		if err := d.Event.Envelope.Validate(); err != nil {
			return err
		}
		if len(d.Event.PreviousError) > 1024 {
			return invalid("event.previous_error", "exceeds 1024 bytes")
		}
		return nil
	}
	if d.Job != nil {
		if err := d.Job.Identity.Validate(); err != nil {
			return err
		}
		if err := validIdentifier("job.handler", d.Job.Handler); err != nil {
			return err
		}
		if d.Job.ScheduledAtUnixMS <= 0 || d.Job.RunID == "" {
			return invalid("job", "scheduled time and run ID are required")
		}
		if len(d.Job.Payload) > MaxJobPayloadBytes || (len(d.Job.Payload) > 0 && !json.Valid(d.Job.Payload)) {
			return invalid("job.payload", "must be bounded valid JSON")
		}
		return nil
	}
	if d.PreSend != nil {
		if err := validIdentifier("pre_send.handler", d.PreSend.Handler); err != nil {
			return err
		}
		if err := d.PreSend.Action.Validate(); err != nil {
			return invalid("pre_send.action", "%v", err)
		}
		if d.PreSend.Action.Kind != ActionSend {
			return invalid("pre_send.action.kind", "must be %q", ActionSend)
		}
		return nil
	}
	if err := d.Update.Validate(); err != nil {
		return err
	}
	if d.Command != nil {
		if err := validIdentifier("command.name", d.Command.Name); err != nil {
			return err
		}
		if len(d.Command.Args) > MaxCollectionItems {
			return invalid("command.args", "has too many entries")
		}
	}
	return nil
}
