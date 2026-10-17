package pluginpackage

import (
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestManifestEventDeclarationsRequireExactCapabilityAndPermission(t *testing.T) {
	manifest := Manifest{Format: FormatV1, ID: "example.events", Name: "Events", Version: "1.0.0", Category: CategoryGoExecutable, Protocol: VersionRange{Min: "1.0"}, Core: VersionRange{Min: "0.1.0"}, Entrypoints: []Entrypoint{{OS: "linux", Arch: "amd64", Path: "bin/plugin"}}, Files: []FileMetadata{{Path: "bin/plugin", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Mode: 0o755}}}
	publication := pluginv1.EventPublication{Type: "example.note.created", Version: 1, MaxPayloadBytes: 1024, DeliveryClass: pluginv1.EventDurable}
	manifest.EventPublications = []pluginv1.EventPublication{publication}
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("event declaration without capability was accepted")
	}
	manifest.Capabilities = append(manifest.Capabilities, string(pluginv1.CapabilityEventsPublish))
	if err := manifest.normalizeAndValidate(); err == nil {
		t.Fatal("event declaration without permission was accepted")
	}
	manifest.Permissions = append(manifest.Permissions, pluginv1.EventPermission("publish", publication.Type, 1))
	if err := manifest.normalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
}
