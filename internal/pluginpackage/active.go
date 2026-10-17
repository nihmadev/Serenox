package pluginpackage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func (m *Manager) writeActive(pluginDirectory string, receipt Receipt) error {
	active := ActiveVersion{Version: receipt.Version, ManifestDigest: receipt.ManifestDigest, UpdatedAt: m.now().UTC()}
	data, err := json.Marshal(active)
	if err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(pluginDirectory, activeFilename), data, 0o600)
}

func readActive(pluginDirectory string) (ActiveVersion, error) {
	data, err := os.ReadFile(filepath.Join(pluginDirectory, activeFilename))
	if err != nil {
		return ActiveVersion{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var active ActiveVersion
	if err := decoder.Decode(&active); err != nil {
		return ActiveVersion{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ActiveVersion{}, err
	}
	if !versionPattern.MatchString(active.Version) || !validManifestDigest(active.ManifestDigest) {
		return ActiveVersion{}, fmt.Errorf("%w: invalid active version pointer", ErrInvalidPackage)
	}
	return active, nil
}
