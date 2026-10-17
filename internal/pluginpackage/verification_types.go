package pluginpackage

import (
	"crypto/ed25519"
	"fmt"
	"runtime"
)

type Limits struct {
	MaxArchiveBytes      int64
	MaxManifestBytes     int64
	MaxSignatureBytes    int64
	MaxFiles             int
	MaxFileBytes         int64
	MaxUncompressedBytes int64
}

func DefaultLimits() Limits {
	return Limits{
		MaxArchiveBytes:      64 << 20,
		MaxManifestBytes:     256 << 10,
		MaxSignatureBytes:    32 << 10,
		MaxFiles:             256,
		MaxFileBytes:         32 << 20,
		MaxUncompressedBytes: 128 << 20,
	}
}

func (l Limits) normalized() (Limits, error) {
	if l == (Limits{}) {
		return DefaultLimits(), nil
	}
	if l.MaxArchiveBytes <= 0 || l.MaxManifestBytes <= 0 || l.MaxSignatureBytes <= 0 ||
		l.MaxFiles <= 0 || l.MaxFileBytes <= 0 || l.MaxUncompressedBytes <= 0 {
		return Limits{}, fmt.Errorf("all package limits must be positive")
	}
	return l, nil
}

type VerifyOptions struct {
	Limits          Limits
	Trust           TrustPolicy
	OS              string
	Arch            string
	CoreVersion     string
	ProtocolVersion string
	CheckPlatform   bool
	CheckVersions   bool
}

func DefaultVerifyOptions() VerifyOptions {
	return VerifyOptions{
		Limits:          DefaultLimits(),
		Trust:           TrustPolicy{Mode: TrustAllowUnsigned},
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CoreVersion:     DefaultCoreVersion,
		ProtocolVersion: "1.0",
		CheckPlatform:   true,
		CheckVersions:   true,
	}
}

type VerifiedPackage struct {
	Manifest       Manifest   `json:"manifest"`
	ManifestDigest string     `json:"manifest_digest"`
	Signed         bool       `json:"signed"`
	SignerKeyID    string     `json:"signer_key_id,omitempty"`
	Trusted        bool       `json:"trusted"`
	Entrypoint     Entrypoint `json:"entrypoint,omitempty"`
	ArchiveSize    int64      `json:"archive_size"`
	Uncompressed   int64      `json:"uncompressed_size"`
}

type PackOptions struct {
	Limits     Limits
	PrivateKey ed25519.PrivateKey
	KeyID      string
}
