package pluginpackage

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	idPattern               = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
	versionPattern          = regexp.MustCompile(`^[0-9A-Za-z](?:[0-9A-Za-z.+_-]{0,62}[0-9A-Za-z])?$`)
	tokenPattern            = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	platformPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	pythonModulePattern     = regexp.MustCompile(`^(?:[a-zA-Z_][a-zA-Z0-9_]*)(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*$|^[a-zA-Z0-9_./-]+\.py$`)
	pythonClassPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	pythonDependencyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	hexDigestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reservedRootNames       = map[string]struct{}{ManifestName: {}, SignatureName: {}}
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
	if m.Category == CategoryPythonSidecar {
		if err := validateRange("sdk", m.SDK); err != nil {
			return err
		}
		if err := validateRange("sidecar", m.Sidecar); err != nil {
			return err
		}
		if err := validateResourceLimits(m.Resources); err != nil {
			return err
		}
		if err := validateDependencies(m.Dependencies); err != nil {
			return err
		}
	} else if m.SDK.Min != "" || m.Sidecar.Min != "" || m.Resources != (ResourceLimits{}) || len(m.Dependencies.Packages) != 0 || m.Dependencies.LockSHA256 != "" {
		return invalid("python", "SDK, sidecar, resources, and dependencies are only valid for Python packages")
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
		if m.Category == CategoryPythonSidecar {
			if err := validatePythonEntrypoint(*e, i); err != nil {
				return err
			}
		}
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
	if len(m.EventPublications) > pluginv1.MaxEventPublications || len(m.EventSubscriptions) > pluginv1.MaxEventSubscriptions {
		return invalid("events", "declarations exceed bounded limits")
	}
	seenPublications := make(map[string]struct{}, len(m.EventPublications))
	for index, declaration := range m.EventPublications {
		if err := declaration.Validate(); err != nil {
			return invalid(fmt.Sprintf("event_publications[%d]", index), "%v", err)
		}
		key := fmt.Sprintf("%s/v%d", declaration.Type, declaration.Version)
		if _, exists := seenPublications[key]; exists {
			return invalid("event_publications", "duplicate declaration %q", key)
		}
		seenPublications[key] = struct{}{}
	}
	seenHandlers := make(map[string]struct{}, len(m.EventSubscriptions))
	for index, declaration := range m.EventSubscriptions {
		if err := declaration.Validate(); err != nil {
			return invalid(fmt.Sprintf("event_subscriptions[%d]", index), "%v", err)
		}
		if _, exists := seenHandlers[declaration.HandlerID]; exists {
			return invalid("event_subscriptions", "duplicate handler %q", declaration.HandlerID)
		}
		seenHandlers[declaration.HandlerID] = struct{}{}
	}
	permissionSet := make(map[string]struct{}, len(m.Permissions))
	for _, permission := range m.Permissions {
		permissionSet[permission] = struct{}{}
	}
	if len(m.EventPublications) > 0 && !containsString(m.Capabilities, string(pluginv1.CapabilityEventsPublish)) {
		return invalid("capabilities", "event publications require events.publish")
	}
	if len(m.EventSubscriptions) > 0 && !containsString(m.Capabilities, string(pluginv1.CapabilityEventsSubscribe)) {
		return invalid("capabilities", "event subscriptions require events.subscribe")
	}
	for _, declaration := range m.EventPublications {
		required := pluginv1.EventPermission("publish", declaration.Type, declaration.Version)
		if _, ok := permissionSet[required]; !ok {
			return invalid("permissions", "event publication requires %q", required)
		}
	}
	for _, declaration := range m.EventSubscriptions {
		required := pluginv1.EventPermission("subscribe", declaration.Type, declaration.Version)
		if _, ok := permissionSet[required]; !ok {
			return invalid("permissions", "event subscription requires %q", required)
		}
	}
	capabilityPermissions := map[string]string{
		"media_read": "media.read", "media_write": "media.write", "media_attach": "media.attach", "jobs": "jobs.schedule",
		"client.identity.read": "client.identity.read", "client.peer.read": "client.peer.read",
		"client.user.read": "client.user.read", "client.chat.read": "client.chat.read",
		"client.message.read": "client.message.read", "client.history.read": "client.history.read",
		"client.search.read": "client.search.read", "client.dialogs.read": "client.dialogs.read",
		"client.participants.read": "client.participants.read", "client.media.metadata": "client.media.metadata",
		"telegram.profile": pluginv1.ProfilePermission,
	}
	moderationCapability := containsString(m.Capabilities, "telegram.moderation")
	moderationPermission := false
	for _, permission := range m.Permissions {
		if strings.HasPrefix(permission, "telegram.moderation.") {
			moderationPermission = true
			if !pluginv1.KnownModerationPermission(permission) {
				return invalid("permissions", "unknown moderation permission %q", permission)
			}
		}
	}
	if moderationCapability && !moderationPermission {
		return invalid("permissions", "capability %q requires at least one granular moderation permission", "telegram.moderation")
	}
	if moderationPermission && !moderationCapability {
		return invalid("capabilities", "moderation permissions require capability %q", "telegram.moderation")
	}
	for _, capability := range m.Capabilities {
		if required, ok := capabilityPermissions[capability]; ok {
			if _, declared := permissionSet[required]; !declared {
				return invalid("permissions", "capability %q requires permission %q", capability, required)
			}
		}
	}
	if err := validateNetworkPolicy(m.Network); err != nil {
		return err
	}
	if containsString(m.Capabilities, "http") {
		_, hasRead := permissionSet["network.http.read"]
		_, hasWrite := permissionSet["network.http.write"]
		if !hasRead && !hasWrite {
			return invalid("permissions", "capability %q requires network.http.read or network.http.write", "http")
		}
	}
	if _, hasHTTP := permissionSet["network.http.read"]; hasHTTP {
		if !containsString(m.Capabilities, "http") {
			return invalid("capabilities", "network.http.read requires capability %q", "http")
		}
	}
	if containsString(m.Capabilities, "http") && len(m.Network.Origins) == 0 {
		return invalid("network.origins", "HTTP capability requires an explicit non-empty origin allowlist")
	}
	if containsString(m.Capabilities, "http") && m.Network.AllowHTTP {
		if _, ok := permissionSet["network.http.cleartext"]; !ok {
			return invalid("permissions", "cleartext HTTP requires permission %q", "network.http.cleartext")
		}
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
		if m.Category == CategoryPythonSidecar && file.Mode != 0o644 {
			return invalid("entrypoints", "Python entrypoint %q must have mode 0644", entryPath)
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

func validatePythonEntrypoint(entry Entrypoint, index int) error {
	if entry.Path == "" || !strings.HasSuffix(strings.ToLower(entry.Path), ".py") {
		return invalid(fmt.Sprintf("entrypoints[%d].path", index), "Python entrypoint must be a .py file")
	}
	if entry.Module == "" {
		return invalid(fmt.Sprintf("entrypoints[%d].module", index), "is required for Python packages")
	}
	if !pythonModulePattern.MatchString(entry.Module) {
		return invalid(fmt.Sprintf("entrypoints[%d].module", index), "must be a local module path or dotted import name")
	}
	expectedModule := strings.TrimSuffix(entry.Path, ".py")
	expectedModule = strings.ReplaceAll(expectedModule, "/", ".")
	if entry.Module != entry.Path && entry.Module != expectedModule {
		return invalid(fmt.Sprintf("entrypoints[%d].module", index), "must identify the declared local Python file")
	}
	if !pythonClassPattern.MatchString(entry.Class) {
		return invalid(fmt.Sprintf("entrypoints[%d].class", index), "must be a single Python class name")
	}
	if strings.HasPrefix(entry.Class, "__") {
		return invalid(fmt.Sprintf("entrypoints[%d].class", index), "dunder classes are not allowed")
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidPackage, field, fmt.Sprintf(format, args...))
}
