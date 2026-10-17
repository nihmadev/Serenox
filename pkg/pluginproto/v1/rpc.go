package v1

type HandshakeRequest struct {
	SupportedVersions []string `json:"supported_versions"`
	Features          []string `json:"features,omitempty"`
	AuthToken         string   `json:"auth_token"`
	InstanceID        string   `json:"instance_id"`
	HostVersion       string   `json:"host_version,omitempty"`
	Nonce             string   `json:"nonce"`
	MaxFrameBytes     uint32   `json:"max_frame_bytes"`
}

type HandshakeResponse struct {
	SelectedVersion string         `json:"selected_version"`
	Nonce           string         `json:"nonce"`
	MaxFrameBytes   uint32         `json:"max_frame_bytes"`
	Metadata        PluginMetadata `json:"metadata"`
}

type LifecycleRequest struct {
	Phase  LifecyclePhase `json:"phase"`
	Config []ConfigValue  `json:"config,omitempty"`
}

type LifecycleResponse struct {
	State   string   `json:"state"`
	Actions []Action `json:"actions,omitempty"`
}

type DispatchRequest struct {
	Update  Update          `json:"update"`
	Command *Command        `json:"command,omitempty"`
	PreSend *PreSendRequest `json:"pre_send,omitempty"`
	Job     *JobInvocation  `json:"job,omitempty"`
	Event   *EventDelivery  `json:"event,omitempty"`
}

type DispatchResponse struct {
	Actions     []Action              `json:"actions,omitempty"`
	PreSend     *PreSendResponse      `json:"pre_send,omitempty"`
	Events      []EventPublishRequest `json:"events,omitempty"`
	EventResult *EventHandlerResult   `json:"event_result,omitempty"`
}

type PreSendRequest struct {
	Handler string `json:"handler"`
	Action  Action `json:"action"`
}

type PreSendResponse struct {
	Action Action `json:"action"`
}

type HealthRequest struct {
	Detailed bool `json:"detailed,omitempty"`
}

type HealthResponse struct {
	Ready    bool              `json:"ready"`
	Status   string            `json:"status"`
	Metrics  map[string]uint64 `json:"metrics,omitempty"`
	UptimeMS int64             `json:"uptime_ms,omitempty"`
}
