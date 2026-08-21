package pluginpackage

import (
	"errors"
	"testing"
)

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
