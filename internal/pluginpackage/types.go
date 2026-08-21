// Package pluginpackage implements the on-disk Serenox plugin package and
// installation format. A .srnx extension is only a user-facing convention;
// callers must always verify the ZIP contents before trusting or extracting it.
package pluginpackage

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	FormatV1            = "serenox.plugin.package/v1"
	SignatureFormatV1   = "serenox.plugin.signature/v1"
	ManifestName        = "manifest.json"
	SignatureName       = "signature.json"
	ReceiptDirectory    = ".serenox"
	DefaultCoreVersion  = "0.1.0"
	DefaultRetain       = 3
	defaultLockWait     = 30
	defaultStaleLockAge = 300
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
	Format       string         `json:"format"`
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Description  string         `json:"description,omitempty"`
	Category     Category       `json:"category"`
	Protocol     VersionRange   `json:"protocol"`
	Core         VersionRange   `json:"core"`
	Entrypoints  []Entrypoint   `json:"entrypoints"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Permissions  []string       `json:"permissions,omitempty"`
	Files        []FileMetadata `json:"files"`
}

type Signature struct {
	Format    string `json:"format"`
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key,omitempty"`
	Digest    string `json:"digest"`
	Value     string `json:"signature"`
}

type TrustMode string

const (
	// TrustAllowUnsigned accepts unsigned packages. A present signature is
	// still verified cryptographically, even when its signer is not trusted.
	TrustAllowUnsigned TrustMode = "allow-unsigned"
	// TrustRequireSigned requires a valid signature but does not require its key
	// to be present in the local trust store.
	TrustRequireSigned TrustMode = "require-signed"
	// TrustRequireTrusted requires a valid signature by a locally trusted key.
	TrustRequireTrusted TrustMode = "require-trusted"
)

type TrustPolicy struct {
	Mode        TrustMode
	TrustedKeys map[string]ed25519.PublicKey
}

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

var (
	ErrInvalidPackage      = errors.New("invalid Serenox plugin package")
	ErrLimitExceeded       = errors.New("plugin package limit exceeded")
	ErrIntegrity           = errors.New("plugin package integrity check failed")
	ErrSignature           = errors.New("plugin package signature check failed")
	ErrUntrusted           = errors.New("plugin signer is not trusted")
	ErrIncompatible        = errors.New("plugin package is incompatible")
	ErrAlreadyInstalled    = errors.New("plugin version is already installed with different contents")
	ErrNotInstalled        = errors.New("plugin is not installed")
	ErrNoRollback          = errors.New("no plugin version is available for rollback")
	ErrInstallLockTimeout  = errors.New("timed out waiting for plugin installation lock")
	ErrUnsafeArchivePath   = errors.New("unsafe plugin archive path")
	ErrUnsupportedFileType = errors.New("unsupported plugin archive file type")
)

var (
	idPattern         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
	versionPattern    = regexp.MustCompile(`^[0-9A-Za-z](?:[0-9A-Za-z.+_-]{0,62}[0-9A-Za-z])?$`)
	tokenPattern      = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	platformPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	hexDigestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reservedRootNames = map[string]struct{}{ManifestName: {}, SignatureName: {}}
)

func (m *Manifest) normalizeAndValidate() error {
	if m.Format == "" {
		m.Format = FormatV1
	}
	if m.Format != FormatV1 {
		return invalid("format", "must be %q", FormatV1)
	}
	if !idPattern.MatchString(m.ID) {
		return invalid("id", "must match %s", idPattern)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 128 {
		return invalid("name", "must contain 1..128 bytes")
	}
	if !versionPattern.MatchString(m.Version) || m.Version == "." || m.Version == ".." {
		return invalid("version", "contains unsupported characters")
	}
	if len(m.Description) > 4096 {
		return invalid("description", "must not exceed 4096 bytes")
	}
	if m.Category != CategoryGoExecutable && m.Category != CategoryPythonSidecar {
		return invalid("category", "must be %q or %q", CategoryGoExecutable, CategoryPythonSidecar)
	}
	if err := validateRange("protocol", m.Protocol); err != nil {
		return err
	}
	if err := validateRange("core", m.Core); err != nil {
		return err
	}
	if len(m.Entrypoints) == 0 || len(m.Entrypoints) > 32 {
		return invalid("entrypoints", "must contain 1..32 records")
	}
	entryKeys := make(map[string]struct{}, len(m.Entrypoints))
	entryPaths := make(map[string]struct{}, len(m.Entrypoints))
	for i := range m.Entrypoints {
		e := &m.Entrypoints[i]
		if !platformPattern.MatchString(e.OS) && e.OS != "any" {
			return invalid(fmt.Sprintf("entrypoints[%d].os", i), "invalid platform token")
		}
		if !platformPattern.MatchString(e.Arch) && e.Arch != "any" {
			return invalid(fmt.Sprintf("entrypoints[%d].arch", i), "invalid platform token")
		}
		cleaned, err := cleanArchivePath(e.Path)
		if err != nil || cleaned != e.Path {
			return invalid(fmt.Sprintf("entrypoints[%d].path", i), "must be a canonical relative archive path")
		}
		key := e.OS + "/" + e.Arch
		if _, exists := entryKeys[key]; exists {
			return invalid("entrypoints", "duplicate platform %q", key)
		}
		entryKeys[key] = struct{}{}
		entryPaths[e.Path] = struct{}{}
	}
	if m.Category == CategoryGoExecutable {
		for _, e := range m.Entrypoints {
			if e.OS == "any" || e.Arch == "any" {
				return invalid("entrypoints", "Go executables require exact os and arch")
			}
		}
	}
	if len(m.Capabilities) > 64 || len(m.Permissions) > 64 {
		return invalid("capabilities/permissions", "each collection is limited to 64 items")
	}
	if err := normalizeTokens("capabilities", &m.Capabilities); err != nil {
		return err
	}
	if err := normalizeTokens("permissions", &m.Permissions); err != nil {
		return err
	}
	if len(m.Files) == 0 {
		return invalid("files", "must not be empty")
	}
	seenPaths := make(map[string]FileMetadata, len(m.Files))
	foldedPaths := make(map[string]string, len(m.Files))
	for i := range m.Files {
		f := &m.Files[i]
		cleaned, err := cleanArchivePath(f.Path)
		if err != nil || cleaned != f.Path {
			return invalid(fmt.Sprintf("files[%d].path", i), "must be a canonical relative archive path")
		}
		if _, reserved := reservedRootNames[f.Path]; reserved || strings.HasPrefix(f.Path, ReceiptDirectory+"/") {
			return invalid(fmt.Sprintf("files[%d].path", i), "uses a reserved package path")
		}
		if _, exists := seenPaths[f.Path]; exists {
			return invalid("files", "duplicate path %q", f.Path)
		}
		folded := strings.ToLower(f.Path)
		if previous, exists := foldedPaths[folded]; exists {
			return invalid("files", "case-colliding paths %q and %q", previous, f.Path)
		}
		seenPaths[f.Path] = *f
		foldedPaths[folded] = f.Path
		if f.Size < 0 {
			return invalid(fmt.Sprintf("files[%d].size", i), "must not be negative")
		}
		if !hexDigestPattern.MatchString(f.SHA256) {
			return invalid(fmt.Sprintf("files[%d].sha256", i), "must be 64 lowercase hexadecimal characters")
		}
		if f.Mode != 0o644 && f.Mode != 0o755 {
			return invalid(fmt.Sprintf("files[%d].mode", i), "must be 0644 or 0755")
		}
	}
	for entryPath := range entryPaths {
		file, exists := seenPaths[entryPath]
		if !exists {
			return invalid("entrypoints", "path %q is not declared in files", entryPath)
		}
		if m.Category == CategoryGoExecutable && file.Mode != 0o755 {
			return invalid("entrypoints", "Go executable %q must have mode 0755", entryPath)
		}
	}
	sort.Slice(m.Entrypoints, func(i, j int) bool {
		if m.Entrypoints[i].OS != m.Entrypoints[j].OS {
			return m.Entrypoints[i].OS < m.Entrypoints[j].OS
		}
		if m.Entrypoints[i].Arch != m.Entrypoints[j].Arch {
			return m.Entrypoints[i].Arch < m.Entrypoints[j].Arch
		}
		return m.Entrypoints[i].Path < m.Entrypoints[j].Path
	})
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return nil
}

func normalizeTokens(field string, values *[]string) error {
	sort.Strings(*values)
	for i, value := range *values {
		if !tokenPattern.MatchString(value) {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "invalid token %q", value)
		}
		if i > 0 && (*values)[i-1] == value {
			return invalid(field, "duplicate token %q", value)
		}
	}
	return nil
}

func validateRange(field string, r VersionRange) error {
	if r.Min == "" {
		return invalid(field+".min", "is required")
	}
	min, err := parseVersion(r.Min)
	if err != nil {
		return invalid(field+".min", "%v", err)
	}
	if r.MaxExclusive == "" {
		return nil
	}
	max, err := parseVersion(r.MaxExclusive)
	if err != nil {
		return invalid(field+".max_exclusive", "%v", err)
	}
	if compareVersion(min, max) >= 0 {
		return invalid(field, "min must be lower than max_exclusive")
	}
	return nil
}

func selectEntrypoint(manifest Manifest, targetOS, targetArch string) (Entrypoint, bool) {
	for _, e := range manifest.Entrypoints {
		if e.OS == targetOS && e.Arch == targetArch {
			return e, true
		}
	}
	if manifest.Category == CategoryPythonSidecar {
		for _, e := range manifest.Entrypoints {
			if (e.OS == targetOS || e.OS == "any") && (e.Arch == targetArch || e.Arch == "any") {
				return e, true
			}
		}
	}
	return Entrypoint{}, false
}

func cleanArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) ||
		strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "", ErrUnsafeArchivePath
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != name {
		return "", ErrUnsafeArchivePath
	}
	first := strings.SplitN(cleaned, "/", 2)[0]
	if strings.Contains(first, ":") {
		return "", ErrUnsafeArchivePath
	}
	return cleaned, nil
}

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidPackage, field, fmt.Sprintf(format, args...))
}
