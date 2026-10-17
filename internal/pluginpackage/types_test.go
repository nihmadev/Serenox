package pluginpackage

import (
	"encoding/json"
	"errors"
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestModerationManifestRequiresExactCapabilityAndPermission(t *testing.T) {
	manifest := Manifest{Format: FormatV1, ID: "example.moderation", Name: "Moderation", Version: "1.0.0", Category: CategoryGoExecutable, Protocol: VersionRange{Min: "1.0"}, Core: VersionRange{Min: "0.1.0"}, Entrypoints: []Entrypoint{{OS: "linux", Arch: "amd64", Path: "bin/plugin"}}, Capabilities: []string{string(pluginv1.CapabilityModeration)}, Permissions: []string{pluginv1.PermissionModerationRestrict}, Files: []FileMetadata{{Path: "bin/plugin", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Mode: 0o755}}}
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatalf("valid moderation manifest: %v", err)
	}
	manifest.Permissions = []string{"telegram.moderation.members.superuser"}
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("unknown moderation permission was accepted")
	}
	manifest.Permissions = []string{pluginv1.PermissionModerationRestrict}
	manifest.Capabilities = nil
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("moderation permission without capability was accepted")
	}
	manifest.Capabilities = []string{string(pluginv1.CapabilityModeration)}
	manifest.Permissions = nil
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("moderation capability without a permission was accepted")
	}
}

func TestPythonManifestRequiresExplicitRuntimeContract(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		Format: FormatV1, ID: "example.python", Name: "Python", Version: "1.0.0",
		Category: CategoryPythonSidecar, Protocol: VersionRange{Min: "1.0", MaxExclusive: "2.0"},
		Core:        VersionRange{Min: "0.1.0", MaxExclusive: "1.0.0"},
		Entrypoints: []Entrypoint{{OS: "any", Arch: "any", Path: "plugin.py"}},
		Files:       []FileMetadata{{Path: "plugin.py", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Mode: 0o644}},
	}
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("Python manifest without SDK, sidecar, module, or class was accepted")
	}
	manifest.SDK = VersionRange{Min: "0.1.0", MaxExclusive: "1.0.0"}
	manifest.Sidecar = manifest.SDK
	manifest.Entrypoints[0].Module = "plugin.py"
	manifest.Entrypoints[0].Class = "Plugin"
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatalf("valid Python manifest: %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil || len(data) == 0 {
		t.Fatalf("marshal Python manifest: %v", err)
	}
}

func TestPythonManifestRejectsUnsafeDependencyMetadata(t *testing.T) {
	manifest := Manifest{Format: FormatV1, ID: "example.python", Name: "Python", Version: "1.0.0", Category: CategoryPythonSidecar, Protocol: VersionRange{Min: "1.0"}, Core: VersionRange{Min: "0.1.0"}, SDK: VersionRange{Min: "0.1.0"}, Sidecar: VersionRange{Min: "0.1.0"}, Entrypoints: []Entrypoint{{OS: "any", Arch: "any", Path: "plugin.py", Module: "plugin.py", Class: "Plugin"}}, Dependencies: DependencyMetadata{Packages: []Dependency{{Name: "bad/name", Version: "1.0.0"}}}, Files: []FileMetadata{{Path: "plugin.py", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Mode: 0o644}}}
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("dependency metadata with a path was accepted")
	}
}

func TestGoEntrypointMustBeExecutable(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		Format: FormatV1, ID: "example.mode", Name: "Mode", Version: "1.0.0",
		Category:    CategoryGoExecutable,
		Protocol:    VersionRange{Min: "1.0", MaxExclusive: "2.0"},
		Core:        VersionRange{Min: "0.1.0", MaxExclusive: "1.0.0"},
		Entrypoints: []Entrypoint{{OS: "linux", Arch: "amd64", Path: "bin/plugin"}},
		Files: []FileMetadata{{
			Path: "bin/plugin", Size: 1,
			SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881",
			Mode:   0o644,
		}},
	}
	if err := manifest.normalizeAndValidate(); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("non-executable Go entrypoint error = %v", err)
	}
	manifest.Files[0].Mode = 0o755
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatalf("executable Go entrypoint: %v", err)
	}
}

func TestHTTPCapabilityRequiresLeastPrivilegeAndOriginPolicy(t *testing.T) {
	manifest := Manifest{Format: FormatV1, ID: "example.http", Name: "HTTP", Version: "1.0.0", Category: CategoryGoExecutable, Protocol: VersionRange{Min: "1.0"}, Core: VersionRange{Min: "0.1.0"}, Entrypoints: []Entrypoint{{OS: "linux", Arch: "amd64", Path: "bin/plugin"}}, Capabilities: []string{"http"}, Permissions: []string{"network.http.read"}, Files: []FileMetadata{{Path: "bin/plugin", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Mode: 0o755}}}
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("HTTP manifest without allowlist was accepted")
	}
	manifest.Network.Origins = []NetworkOrigin{{Scheme: "https", Host: "api.example.com"}}
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatalf("valid read-only HTTP manifest: %v", err)
	}
	manifest.Permissions = []string{"network.http.write"}
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatalf("valid write-only HTTP manifest: %v", err)
	}
	manifest.Permissions = nil
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("HTTP capability without HTTP permission was accepted")
	}
}
