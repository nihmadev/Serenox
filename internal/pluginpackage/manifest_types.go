package pluginpackage

import (
	"encoding/json"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Category string

const (
	CategoryGoExecutable  Category = "go-executable"
	CategoryPythonSidecar Category = "python-sidecar"
)

type VersionRange struct {
	Min          string `json:"min"`
	MaxExclusive string `json:"max_exclusive,omitempty"`
}

type Entrypoint struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Path string `json:"path"`
	// Module and Class are required for Python entrypoints. Keeping them on
	// the platform entrypoint makes the selected import unambiguous and lets
	// one package publish different Python modules per platform when needed.
	Module string `json:"module,omitempty"`
	Class  string `json:"class,omitempty"`
}

// ResourceLimits are package-level lower bounds for the sidecar's finite
// runtime. The daemon may apply stricter local limits, but never raises a
// package declaration. Zero means "use the product default" for optional
// limits.
type ResourceLimits struct {
	QueueCapacity          int   `json:"queue_capacity,omitempty"`
	Workers                int   `json:"workers,omitempty"`
	ActionBatch            int   `json:"action_batch,omitempty"`
	ActionBytes            int   `json:"action_bytes,omitempty"`
	StorageEntries         int   `json:"storage_entries,omitempty"`
	StorageValueBytes      int   `json:"storage_value_bytes,omitempty"`
	CallbackCapacity       int   `json:"callback_capacity,omitempty"`
	MaxFrameBytes          int   `json:"max_frame_bytes,omitempty"`
	HandlerTimeoutMS       int   `json:"handler_timeout_ms,omitempty"`
	ShutdownTimeoutMS      int   `json:"shutdown_timeout_ms,omitempty"`
	MediaMaxSizeBytes      int64 `json:"media_max_size_bytes,omitempty"`
	MediaChunkSizeBytes    int   `json:"media_chunk_size_bytes,omitempty"`
	MediaMaxConcurrent     int   `json:"media_max_concurrent,omitempty"`
	MediaQuotaBytes        int64 `json:"media_quota_bytes,omitempty"`
	MediaTransferTimeoutMS int   `json:"media_transfer_timeout_ms,omitempty"`
	MediaIdleTimeoutMS     int   `json:"media_idle_timeout_ms,omitempty"`
	MaxJobs                int   `json:"max_jobs,omitempty"`
	MaxJobPayloadBytes     int   `json:"max_job_payload_bytes,omitempty"`
	MaxJobRuntimeMS        int   `json:"max_job_runtime_ms,omitempty"`
}

// NetworkOrigin is one verified outbound HTTP origin. Host matching is exact
// unless Subdomains is true; a port of zero means the scheme's default port.
type NetworkOrigin struct {
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	Port       int    `json:"port,omitempty"`
	Subdomains bool   `json:"subdomains,omitempty"`
	Name       string `json:"name,omitempty"`
}

// NetworkPolicy is signed package policy, not plugin-provided runtime input.
// An empty policy denies all network requests.
type NetworkPolicy struct {
	Origins                  []NetworkOrigin `json:"origins,omitempty"`
	AllowHTTP                bool            `json:"allow_http,omitempty"`
	AllowCrossOriginRedirect bool            `json:"allow_cross_origin_redirect,omitempty"`
	MaxRequestBytes          int64           `json:"max_request_bytes,omitempty"`
	MaxResponseBytes         int64           `json:"max_response_bytes,omitempty"`
	MaxRedirects             int             `json:"max_redirects,omitempty"`
	RequestTimeoutMS         int             `json:"request_timeout_ms,omitempty"`
	ConnectionTimeoutMS      int             `json:"connection_timeout_ms,omitempty"`
	HeaderTimeoutMS          int             `json:"header_timeout_ms,omitempty"`
	IdleTimeoutMS            int             `json:"idle_timeout_ms,omitempty"`
	MaxConcurrent            int             `json:"max_concurrent,omitempty"`
	RequestsPerSecond        float64         `json:"requests_per_second,omitempty"`
	Burst                    int             `json:"burst,omitempty"`
	TotalByteBudget          int64           `json:"total_byte_budget,omitempty"`
	AllowCookies             bool            `json:"allow_cookies,omitempty"`
	AllowAuthorization       bool            `json:"allow_authorization,omitempty"`
	AllowCustomUserAgent     bool            `json:"allow_custom_user_agent,omitempty"`
}

// Dependency describes an already-packaged, non-network dependency. The
// verifier records it as metadata only; startup never invokes a package
// manager or executes dependency setup hooks.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type DependencyMetadata struct {
	Packages   []Dependency `json:"packages,omitempty"`
	LockSHA256 string       `json:"lock_sha256,omitempty"`
}

type FileMetadata struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// Manifest is the canonical, signed package declaration. Files contains every
// payload file and excludes the reserved manifest and signature entries.
type Manifest struct {
	Format      string       `json:"format"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description,omitempty"`
	Category    Category     `json:"category"`
	Protocol    VersionRange `json:"protocol"`
	Core        VersionRange `json:"core"`
	// SDK is the Python SDK compatibility range. It is required for Python
	// packages and omitted for native Go packages.
	SDK VersionRange `json:"sdk,omitempty"`
	// Sidecar is the compatibility-host range required by Python packages.
	Sidecar            VersionRange                 `json:"sidecar,omitempty"`
	Entrypoints        []Entrypoint                 `json:"entrypoints"`
	Capabilities       []string                     `json:"capabilities,omitempty"`
	Permissions        []string                     `json:"permissions,omitempty"`
	EventPublications  []pluginv1.EventPublication  `json:"event_publications,omitempty"`
	EventSubscriptions []pluginv1.EventSubscription `json:"event_subscriptions,omitempty"`
	Network            NetworkPolicy                `json:"network,omitempty"`
	Resources          ResourceLimits               `json:"resources,omitempty"`
	Dependencies       DependencyMetadata           `json:"dependencies,omitempty"`
	Files              []FileMetadata               `json:"files"`
}

// MarshalJSON keeps v1 manifests backwards-readable for native packages:
// optional Python-only objects are absent rather than serialized as empty
// objects. This also keeps canonical bytes stable for existing Go packages.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type plain Manifest
	value := plain(m)
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if m.Category != CategoryPythonSidecar {
		delete(object, "sdk")
		delete(object, "sidecar")
		delete(object, "resources")
		delete(object, "dependencies")
	}
	if networkPolicyEmpty(m.Network) {
		delete(object, "network")
	}
	if m.Category == CategoryPythonSidecar && m.Resources == (ResourceLimits{}) {
		delete(object, "resources")
	}
	if m.Category == CategoryPythonSidecar && len(m.Dependencies.Packages) == 0 && m.Dependencies.LockSHA256 == "" {
		delete(object, "dependencies")
	}
	return json.Marshal(object)
}

func networkPolicyEmpty(policy NetworkPolicy) bool {
	return len(policy.Origins) == 0 && !policy.AllowHTTP && !policy.AllowCrossOriginRedirect && policy.MaxRequestBytes == 0 && policy.MaxResponseBytes == 0 && policy.MaxRedirects == 0 && policy.RequestTimeoutMS == 0 && policy.ConnectionTimeoutMS == 0 && policy.HeaderTimeoutMS == 0 && policy.IdleTimeoutMS == 0 && policy.MaxConcurrent == 0 && policy.RequestsPerSecond == 0 && policy.Burst == 0 && policy.TotalByteBudget == 0 && !policy.AllowCookies && !policy.AllowAuthorization && !policy.AllowCustomUserAgent
}
