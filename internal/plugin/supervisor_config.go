package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// RestartPolicy bounds both retained history and restart frequency.
type RestartPolicy struct {
	Disabled       bool
	MaxRestarts    int
	Window         time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	JitterFraction float64
	DisableJitter  bool
}

func (p RestartPolicy) normalized() (RestartPolicy, error) {
	if p.MaxRestarts == 0 {
		p.MaxRestarts = 4
	}
	if p.Window == 0 {
		p.Window = 5 * time.Minute
	}
	if p.InitialBackoff == 0 {
		p.InitialBackoff = 100 * time.Millisecond
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = 5 * time.Second
	}
	if p.JitterFraction == 0 && !p.DisableJitter {
		p.JitterFraction = 0.1
	}
	if p.MaxRestarts < 1 || p.MaxRestarts > 1024 {
		return RestartPolicy{}, fmt.Errorf("restart max must be in range 1..1024")
	}
	if p.Window < time.Millisecond || p.Window > 30*24*time.Hour {
		return RestartPolicy{}, fmt.Errorf("restart window must be between 1ms and 30 days")
	}
	if p.InitialBackoff < time.Millisecond || p.InitialBackoff > time.Hour {
		return RestartPolicy{}, fmt.Errorf("initial restart backoff must be between 1ms and 1h")
	}
	if p.MaxBackoff < p.InitialBackoff || p.MaxBackoff > 24*time.Hour {
		return RestartPolicy{}, fmt.Errorf("maximum restart backoff must be between initial backoff and 24h")
	}
	if p.JitterFraction < 0 || p.JitterFraction > 1 {
		return RestartPolicy{}, fmt.Errorf("restart jitter fraction must be in range 0..1")
	}
	return p, nil
}

func (c SupervisorConfig) normalized() (SupervisorConfig, error) {
	if c.Command == "" {
		c.Command = c.Executable
	}
	if strings.TrimSpace(c.Command) == "" {
		return SupervisorConfig{}, fmt.Errorf("plugin command is required")
	}
	if len(c.Args) > protocol.MaxCollectionItems || len(c.Env) > protocol.MaxCollectionItems {
		return SupervisorConfig{}, fmt.Errorf("plugin arguments and environment are limited to %d entries", protocol.MaxCollectionItems)
	}
	if c.InstanceID == "" {
		random, err := randomHex(8)
		if err != nil {
			return SupervisorConfig{}, fmt.Errorf("generate plugin instance ID: %w", err)
		}
		c.InstanceID = "plugin-" + random
	}
	probe := protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: "validation-token",
		InstanceID: c.InstanceID, Nonce: "validation-nonce", MaxFrameBytes: 1,
	}
	if err := probe.Validate(); err != nil {
		return SupervisorConfig{}, fmt.Errorf("invalid plugin instance ID: %w", err)
	}
	if c.ExpectedPluginID != "" && len(c.ExpectedPluginID) > 128 {
		return SupervisorConfig{}, fmt.Errorf("expected plugin ID exceeds 128 bytes")
	}
	if c.MaxFrameSize == 0 {
		c.MaxFrameSize = protocol.DefaultMaxFrameSize
	}
	if c.MaxFrameSize < 1024 || c.MaxFrameSize > 64<<20 {
		return SupervisorConfig{}, fmt.Errorf("maximum frame size must be between 1KiB and 64MiB")
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = 8
	}
	if c.MaxInFlight < 1 || c.MaxInFlight > 1024 {
		return SupervisorConfig{}, fmt.Errorf("maximum in-flight RPCs must be in range 1..1024")
	}

	setDurationDefault(&c.HandshakeTimeout, 5*time.Second)
	setDurationDefault(&c.ConfigureTimeout, 10*time.Second)
	setDurationDefault(&c.StartTimeout, 10*time.Second)
	setDurationDefault(&c.HealthTimeout, 3*time.Second)
	setDurationDefault(&c.DispatchTimeout, 15*time.Second)
	setDurationDefault(&c.StopTimeout, 5*time.Second)
	setDurationDefault(&c.TerminateTimeout, 2*time.Second)
	setDurationDefault(&c.HealthInterval, 30*time.Second)
	for name, value := range map[string]time.Duration{
		"handshake timeout": c.HandshakeTimeout, "configure timeout": c.ConfigureTimeout,
		"start timeout": c.StartTimeout, "health timeout": c.HealthTimeout,
		"dispatch timeout": c.DispatchTimeout, "stop timeout": c.StopTimeout,
		"termination timeout": c.TerminateTimeout, "health interval": c.HealthInterval,
	} {
		if value < time.Millisecond || value > 24*time.Hour {
			return SupervisorConfig{}, fmt.Errorf("%s must be between 1ms and 24h", name)
		}
	}
	if c.OutputLimitBytes == 0 {
		c.OutputLimitBytes = 64 << 10
	}
	if c.OutputLimitBytes < 1024 || c.OutputLimitBytes > 16<<20 {
		return SupervisorConfig{}, fmt.Errorf("process output limit must be between 1KiB and 16MiB")
	}
	if c.Transport == nil {
		c.Transport = UnixTransport{}
	}
	if c.EndpointFactory == nil {
		c.EndpointFactory = func(runtimeDir, _ string) (string, error) {
			candidate := filepath.Join(runtimeDir, "plugin.sock")
			// sockaddr_un is commonly limited to 104-108 bytes. Tests, deeply
			// nested deployments, and long usernames can exceed that even with
			// a short basename. Keep the fallback deterministic per runtime dir.
			if len(candidate) <= 96 {
				return candidate, nil
			}
			digest := sha256.Sum256([]byte(runtimeDir))
			return filepath.Join(os.TempDir(), "srnx-"+hex.EncodeToString(digest[:12])+".sock"), nil
		}
	}
	if c.RuntimeBaseDir == "" {
		c.RuntimeBaseDir = os.TempDir()
	}
	restart, err := c.Restart.normalized()
	if err != nil {
		return SupervisorConfig{}, err
	}
	c.Restart = restart
	c.Args = append([]string(nil), c.Args...)
	c.Env = append([]string(nil), c.Env...)
	c.Values = cloneConfigValues(c.Values)
	return c, nil
}

func setDurationDefault(value *time.Duration, fallback time.Duration) {
	if *value == 0 {
		*value = fallback
	}
}
