package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nihmadev/Serenox/internal/pythonruntime"
)

// pythonRuntime locates the supported sidecar without forcing every local
// development install to duplicate a python_executable setting. Explicit
// configuration always wins; the bundled uv environment is the preferred
// automatic choice.
type pythonRuntime struct {
	executable string
	enabled    bool
	env        []string
	info       pythonruntime.Info
	err        error
}

func discoverPythonRuntimeContract(configured, managed string, projectRoots []string) pythonRuntime {
	info, err := pythonruntime.Discover(context.Background(), pythonruntime.Policy{
		Executable: configured, ManagedRuntime: managed,
	}, projectRoots)
	if err != nil {
		return pythonRuntime{err: err}
	}
	env := []string{}
	if info.SourceRoot != "" {
		env = sidecarSourceEnv(info.SourceRoot)
	}
	return pythonRuntime{executable: info.Executable, enabled: true, env: env, info: info}
}

func discoverPythonRuntime(configured string) pythonRuntime {
	projectRoots := []string{"."}
	if executable, err := os.Executable(); err == nil {
		binaryDirectory := filepath.Dir(executable)
		projectRoots = append(projectRoots, filepath.Join(binaryDirectory, ".."))
	}
	return discoverPythonRuntimeFromRoots(configured, projectRoots)
}

func discoverPythonRuntimeFromRoots(configured string, projectRoots []string) pythonRuntime {
	if configured = strings.TrimSpace(configured); configured != "" {
		return pythonRuntime{executable: configured, enabled: true}
	}

	seen := make(map[string]struct{})
	for _, root := range projectRoots {
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		candidate := filepath.Join(root, "sidecar", "python", ".venv", "bin", "python")
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		if isExecutableFile(candidate) {
			return pythonRuntime{
				executable: candidate, enabled: true,
				env: sidecarSourceEnv(filepath.Join(root, "sidecar", "python", "src")),
			}
		}
	}

	// A globally installed sidecar is also a valid deployment. The source path
	// is added when running from a source checkout so python3 can import the
	// package even without an editable uv install.
	if executable, err := exec.LookPath("python3"); err == nil {
		var env []string
		if sourceRoot, ok := findSidecarSource(projectRoots); ok {
			env = sidecarSourceEnv(sourceRoot)
		}
		return pythonRuntime{executable: executable, enabled: true, env: env}
	}
	return pythonRuntime{}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func findSidecarSource(projectRoots []string) (string, bool) {
	for _, root := range projectRoots {
		candidate, err := filepath.Abs(filepath.Join(root, "sidecar", "python", "src"))
		if err == nil {
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				return candidate, true
			}
		}
	}
	return "", false
}

func sidecarSourceEnv(source string) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	value := source
	return []string{"PYTHONPATH=" + value}
}
