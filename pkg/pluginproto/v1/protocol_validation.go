package v1

func ValidateRequest(r Request) error {
	if r.Protocol != Protocol {
		return invalid("protocol", "must be %q", Protocol)
	}
	if r.RequestID == 0 {
		return invalid("request_id", "must be non-zero")
	}
	if r.Kind != KindRequest {
		return invalid("kind", "must be %q", KindRequest)
	}
	if r.DeadlineUnixMS <= 0 {
		return invalid("deadline_unix_ms", "must be positive")
	}
	switch r.Method {
	case MethodHandshake, MethodConfigure, MethodStart, MethodStop, MethodHealth, MethodDispatch,
		MethodHostActions, MethodHostStorage, MethodHostClient, MethodHostMedia, MethodHostHTTP, MethodHostJobs, MethodHostModeration, MethodHostEvents:
	default:
		return invalid("method", "unknown method %q", r.Method)
	}
	if len(r.Payload) > int(DefaultMaxFrameSize) {
		return invalid("payload", "exceeds protocol payload limit")
	}
	return nil
}

func ValidateResponse(r Response) error {
	if r.Protocol != Protocol {
		return invalid("protocol", "must be %q", Protocol)
	}
	if r.RequestID == 0 {
		return invalid("request_id", "must be non-zero")
	}
	if r.Kind != KindResponse {
		return invalid("kind", "must be %q", KindResponse)
	}
	if r.DeadlineUnixMS <= 0 {
		return invalid("deadline_unix_ms", "must be positive")
	}
	if r.Error != nil && len(r.Result) != 0 {
		return invalid("result", "must be empty when error is present")
	}
	if r.Error != nil {
		return r.Error.Validate()
	}
	return nil
}

func (e Error) Validate() error {
	if e.Code == "" {
		return invalid("error.code", "is required")
	}
	if err := validString("error.message", e.Message, true); err != nil {
		return err
	}
	if err := validIdentifier("error.operation", e.Operation); err != nil && e.Operation != "" {
		return err
	}
	if e.FloodWaitSeconds < 0 || e.FloodWaitSeconds > 7*24*60*60 {
		return invalid("error.flood_wait_seconds", "is outside its bound")
	}
	if err := validString("error.field", e.Field, false); err != nil {
		return err
	}
	if len(e.Details) > MaxActionMetadata {
		return invalid("error.details", "has too many entries")
	}
	for key, value := range e.Details {
		if err := validIdentifier("error.details.key", key); err != nil {
			return err
		}
		if err := validString("error.details.value", value, false); err != nil {
			return err
		}
		if len(value) > MaxActionMetadataValueBytes {
			return invalid("error.details.value", "exceeds %d bytes", MaxActionMetadataValueBytes)
		}
	}
	return nil
}
