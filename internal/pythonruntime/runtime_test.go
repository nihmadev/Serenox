package pythonruntime

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

func TestDiscoverPythonRuntimeReportsContract(t *testing.T) {
	executable, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	info, err := Discover(context.Background(), Policy{Executable: executable}, []string{filepath.Join("..", "..")})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if info.PythonVersion == "" || info.SidecarVersion != SidecarVersion || info.SDKVersion != SDKVersion || info.Protocol != ProtocolVersion {
		t.Fatalf("runtime info = %#v", info)
	}
}

func TestValidateManifestReportsStableCompatibilityErrors(t *testing.T) {
	info := Info{Executable: "/python", PythonVersion: "3.12.0", SidecarVersion: SidecarVersion, SDKVersion: SDKVersion, Protocol: ProtocolVersion}
	manifest := pluginpackage.Manifest{Category: pluginpackage.CategoryPythonSidecar, Protocol: pluginpackage.VersionRange{Min: "1.0"}, SDK: pluginpackage.VersionRange{Min: "9.0"}, Sidecar: pluginpackage.VersionRange{Min: SidecarVersion}}
	err := ValidateManifest(info, manifest)
	if err == nil || !strings.Contains(err.Error(), "SDK") || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("compatibility error = %v", err)
	}
}
