// Package pythonruntime defines the supported, process-isolated Python
// runtime contract. It performs discovery and probing only; it never embeds
// Python and never installs packages at startup.
package pythonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
)

const (
	MinimumPythonVersion = "3.10.0"
	SidecarVersion       = "0.1.0"
	SDKVersion           = "0.1.0"
	ProtocolVersion      = "1.0"
)

var ErrUnavailable = errors.New("supported Python runtime is unavailable")

type Policy struct {
	Executable     string
	ManagedRuntime string
	Environment    string
	Dependencies   string
}

type Info struct {
	Executable     string `json:"interpreter_path"`
	PythonVersion  string `json:"interpreter_version"`
	SidecarVersion string `json:"sidecar_version"`
	SDKVersion     string `json:"sdk_version"`
	Protocol       string `json:"protocol_version"`
	SourceRoot     string `json:"source_root,omitempty"`
}

func (i Info) Available() bool { return i.Executable != "" }

// Discover searches only configured, managed, bundled, and explicit system
// locations. The returned error is actionable but does not prevent the daemon
// from running when no Python package is loaded.
func Discover(ctx context.Context, policy Policy, roots []string) (Info, error) {
	candidates := make([]string, 0, len(roots)+3)
	if value := strings.TrimSpace(policy.Executable); value != "" {
		candidates = append(candidates, value)
	}
	if value := strings.TrimSpace(policy.ManagedRuntime); value != "" {
		candidates = append(candidates, runtimeExecutable(value))
	}
	for _, root := range roots {
		if absolute, err := filepath.Abs(root); err == nil {
			candidates = append(candidates, filepath.Join(absolute, "sidecar", "python", ".venv", "bin", "python"))
		}
	}
	if system, err := exec.LookPath("python3"); err == nil {
		candidates = append(candidates, system)
	}
	seen := make(map[string]struct{}, len(candidates))
	var lastErr error
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		if !isExecutable(candidate) {
			lastErr = fmt.Errorf("%w: interpreter %q is not executable", ErrUnavailable, candidate)
			continue
		}
		info, err := probe(ctx, candidate, roots)
		if err != nil {
			lastErr = err
			continue
		}
		return info, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: no Python 3 interpreter was found", ErrUnavailable)
	}
	return Info{}, lastErr
}

func ValidateManifest(info Info, manifest pluginpackage.Manifest) error {
	if manifest.Category != pluginpackage.CategoryPythonSidecar {
		return nil
	}
	if !info.Available() {
		return fmt.Errorf("%w: configure plugins.python_executable or plugins.managed_runtime", ErrUnavailable)
	}
	for field, value := range map[string]string{
		"python": info.PythonVersion, "sidecar": info.SidecarVersion, "SDK": info.SDKVersion,
	} {
		if value == "" {
			return fmt.Errorf("%w: %s version was not reported", ErrUnavailable, field)
		}
	}
	ok, err := pluginpackage.VersionInRange(info.Protocol, manifest.Protocol)
	if err != nil || !ok {
		return fmt.Errorf("%w: protocol %q is outside package range", pluginpackage.ErrIncompatible, info.Protocol)
	}
	ok, err = pluginpackage.VersionInRange(info.SidecarVersion, manifest.Sidecar)
	if err != nil || !ok {
		return fmt.Errorf("%w: sidecar %q is outside package range [%s,%s)", pluginpackage.ErrIncompatible, info.SidecarVersion, manifest.Sidecar.Min, manifest.Sidecar.MaxExclusive)
	}
	ok, err = pluginpackage.VersionInRange(info.SDKVersion, manifest.SDK)
	if err != nil || !ok {
		return fmt.Errorf("%w: SDK %q is outside package range [%s,%s)", pluginpackage.ErrIncompatible, info.SDKVersion, manifest.SDK.Min, manifest.SDK.MaxExclusive)
	}
	return nil
}

func runtimeExecutable(value string) string {
	info, err := os.Stat(value)
	if err == nil && info.IsDir() {
		name := "python"
		if runtime.GOOS == "windows" {
			name = "python.exe"
		}
		return filepath.Join(value, "bin", name)
	}
	return value
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0)
}

func probe(parent context.Context, executable string, roots []string) (Info, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	code := `import json,sys
result={"python":"%d.%d.%d" % sys.version_info[:3]}
try:
 import serenox_sidecar
 result["sidecar"]=getattr(serenox_sidecar,"__version__","")
except Exception:
 result["sidecar"]=""
try:
 from serenox_sdk.protocol import SDK_VERSION,PROTOCOL_VERSION
 result["sdk"]=SDK_VERSION; result["protocol"]=PROTOCOL_VERSION
except Exception:
 result["sdk"]=""; result["protocol"]=""
print(json.dumps(result,separators=(",",":")))`
	command := exec.CommandContext(ctx, executable, "-c", code)
	command.Env = probeEnvironment(executable, roots)
	output, err := command.Output()
	if err != nil {
		return Info{}, fmt.Errorf("%w: probe %q: %v", ErrUnavailable, executable, err)
	}
	var values struct {
		Python, Sidecar, SDK, Protocol string
	}
	if err := json.Unmarshal(output, &values); err != nil {
		return Info{}, fmt.Errorf("%w: invalid interpreter probe response: %v", ErrUnavailable, err)
	}
	pythonOK, err := pluginpackage.VersionInRange(values.Python, pluginpackage.VersionRange{Min: MinimumPythonVersion})
	if err != nil || !pythonOK {
		return Info{}, fmt.Errorf("%w: Python %q is below the supported minimum %s", ErrUnavailable, values.Python, MinimumPythonVersion)
	}
	if values.Sidecar == "" || values.SDK == "" || values.Protocol == "" {
		return Info{}, fmt.Errorf("%w: %q cannot import the Serenox sidecar and SDK", ErrUnavailable, executable)
	}
	return Info{Executable: executable, PythonVersion: values.Python, SidecarVersion: values.Sidecar, SDKVersion: values.SDK, Protocol: values.Protocol, SourceRoot: findSourceRoot(roots)}, nil
}

func probeEnvironment(executable string, roots []string) []string {
	// The probe intentionally receives no ambient environment. Only the
	// sidecar source path needed by a source checkout is supplied explicitly.
	for _, root := range roots {
		if absolute, err := filepath.Abs(root); err == nil {
			source := filepath.Join(absolute, "sidecar", "python", "src")
			if info, statErr := os.Stat(source); statErr == nil && info.IsDir() {
				return []string{"PYTHONPATH=" + source, "PATH=" + filepath.Dir(executable)}
			}
		}
	}
	return []string{"PATH=" + filepath.Dir(executable)}
}

func findSourceRoot(roots []string) string {
	for _, root := range roots {
		if absolute, err := filepath.Abs(root); err == nil {
			source := filepath.Join(absolute, "sidecar", "python", "src")
			if info, statErr := os.Stat(source); statErr == nil && info.IsDir() {
				return source
			}
		}
	}
	return ""
}
