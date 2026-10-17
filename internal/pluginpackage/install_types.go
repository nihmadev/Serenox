package pluginpackage

import (
	"context"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	pluginsDirectory  = "plugins"
	versionsDirectory = "versions"
	activeFilename    = "active.json"
	receiptFilename   = "receipt.json"
	lockFilename      = ".plugin-install.lock"
)

type ManagerConfig struct {
	Root             string
	Verify           VerifyOptions
	RetainVersions   int
	LockWait         time.Duration
	StaleLockAge     time.Duration
	RuntimeValidator func(context.Context, Manifest) error
}

type Manager struct {
	root             string
	verify           VerifyOptions
	retainVersions   int
	lockWait         time.Duration
	staleLockAge     time.Duration
	runtimeValidator func(context.Context, Manifest) error
	now              func() time.Time
}

// RetainVersions reports the fixed installation retention bound. Runtime
// activation controllers require at least two so a failed upgrade can restore
// the previously active bytes; the standalone CLI may intentionally use one.
func (m *Manager) RetainVersions() int {
	if m == nil {
		return 0
	}
	return m.retainVersions
}

type Receipt struct {
	Format             string                       `json:"format"`
	ID                 string                       `json:"id"`
	Name               string                       `json:"name"`
	Version            string                       `json:"version"`
	Category           Category                     `json:"category"`
	ManifestDigest     string                       `json:"manifest_digest"`
	Entrypoint         Entrypoint                   `json:"entrypoint"`
	Protocol           VersionRange                 `json:"protocol,omitempty"`
	SDK                VersionRange                 `json:"sdk,omitempty"`
	Sidecar            VersionRange                 `json:"sidecar,omitempty"`
	Resources          ResourceLimits               `json:"resources,omitempty"`
	Dependencies       DependencyMetadata           `json:"dependencies,omitempty"`
	Capabilities       []string                     `json:"capabilities,omitempty"`
	Permissions        []string                     `json:"permissions,omitempty"`
	EventPublications  []pluginv1.EventPublication  `json:"event_publications,omitempty"`
	EventSubscriptions []pluginv1.EventSubscription `json:"event_subscriptions,omitempty"`
	Network            NetworkPolicy                `json:"network,omitempty"`
	Signed             bool                         `json:"signed"`
	SignerKeyID        string                       `json:"signer_key_id,omitempty"`
	Trusted            bool                         `json:"trusted"`
	InstalledAt        time.Time                    `json:"installed_at"`
}

type ActiveVersion struct {
	Version        string    `json:"version"`
	ManifestDigest string    `json:"manifest_digest"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type InstalledVersion struct {
	Receipt Receipt `json:"receipt"`
	Path    string  `json:"path"`
	Active  bool    `json:"active"`
}

type InstalledPlugin struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Category      Category           `json:"category"`
	ActiveVersion string             `json:"active_version,omitempty"`
	Versions      []InstalledVersion `json:"versions"`
}

type installLock struct {
	PID       int       `json:"pid"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}
