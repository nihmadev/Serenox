package pluginpackage

import (
	"fmt"
	"path/filepath"
	"runtime"
	"time"
)

func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Root == "" {
		return nil, fmt.Errorf("plugin installation root is required")
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin installation root: %w", err)
	}
	limits, err := config.Verify.Limits.normalized()
	if err != nil {
		return nil, err
	}
	config.Verify.Limits = limits
	if config.Verify.Trust.Mode == "" {
		config.Verify.Trust.Mode = TrustAllowUnsigned
	}
	if err := validateTrustMode(config.Verify.Trust.Mode); err != nil {
		return nil, err
	}
	if config.Verify.OS == "" {
		config.Verify.OS = runtime.GOOS
	}
	if config.Verify.Arch == "" {
		config.Verify.Arch = runtime.GOARCH
	}
	if config.Verify.CoreVersion == "" {
		config.Verify.CoreVersion = DefaultCoreVersion
	}
	if config.Verify.ProtocolVersion == "" {
		config.Verify.ProtocolVersion = "1.0"
	}
	config.Verify.CheckPlatform = true
	config.Verify.CheckVersions = true
	if config.RetainVersions == 0 {
		config.RetainVersions = DefaultRetain
	}
	if config.RetainVersions < 1 || config.RetainVersions > 32 {
		return nil, fmt.Errorf("retain versions must be between 1 and 32")
	}
	if config.LockWait == 0 {
		config.LockWait = defaultLockWait * time.Second
	}
	if config.StaleLockAge == 0 {
		config.StaleLockAge = defaultStaleLockAge * time.Second
	}
	if config.LockWait < 0 || config.StaleLockAge < time.Second {
		return nil, fmt.Errorf("lock wait must be non-negative and stale lock age at least one second")
	}
	return &Manager{
		root: root, verify: config.Verify, retainVersions: config.RetainVersions,
		lockWait: config.LockWait, staleLockAge: config.StaleLockAge,
		runtimeValidator: config.RuntimeValidator, now: time.Now,
	}, nil
}

func (m *Manager) pluginDirectory(id string) string {
	return filepath.Join(m.root, pluginsDirectory, id)
}
