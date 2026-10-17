package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPythonRuntimeHonorsExplicitExecutable(t *testing.T) {
	runtime := discoverPythonRuntime("/opt/serenox/python")
	if !runtime.enabled || runtime.executable != "/opt/serenox/python" || len(runtime.env) != 0 {
		t.Fatalf("explicit Python runtime = %#v", runtime)
	}
}

func TestDiscoverPythonRuntimeFindsBundledEnvironment(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "sidecar", "python", ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := discoverPythonRuntimeFromRoots("", []string{root})
	if !runtime.enabled || runtime.executable != candidate || len(runtime.env) != 1 {
		t.Fatalf("bundled Python runtime = %#v", runtime)
	}
}
