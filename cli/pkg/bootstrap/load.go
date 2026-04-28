package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/cli/pkg/inventory"

	"gopkg.in/yaml.v3"
)

// LoadOverlay parses a YAML overlay file at path. Returns nil + nil when path is
// empty so callers can pass `manifest.BootstrapOverlay` blindly. The decoder is
// strict (KnownFields(true)) — typos and stale fields fail parse, which is the
// only schema-evolution check we want at this stage.
func LoadOverlay(path string) (*Overlay, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read overlay %s: %w", path, err)
	}
	var o Overlay
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("parse overlay %s: %w", path, err)
	}
	return &o, nil
}

// resolveOverlayPath resolves the overlay path relative to the manifest directory.
// Absolute paths pass through unchanged. Empty input returns empty.
func resolveOverlayPath(manifestDir, overlayPath string) string {
	if overlayPath == "" {
		return ""
	}
	if filepath.IsAbs(overlayPath) {
		return overlayPath
	}
	return filepath.Join(manifestDir, overlayPath)
}

// RenderFromManifest is the production entrypoint: it derives the manifest layer,
// loads the overlay declared in `bootstrap_overlay`, merges, and resolves secrets.
// manifestDir is the directory the overlay path is resolved relative to (typically
// `filepath.Dir(manifestPath)`).
func RenderFromManifest(m *inventory.Manifest, manifestDir string, opts DeriveOptions, resolver Resolver) (*Rendered, error) {
	if m == nil {
		return nil, fmt.Errorf("RenderFromManifest: nil manifest")
	}
	derived, err := Derive(m, opts)
	if err != nil {
		return nil, fmt.Errorf("derive: %w", err)
	}
	overlay, err := LoadOverlay(resolveOverlayPath(manifestDir, m.BootstrapOverlay))
	if err != nil {
		return nil, err
	}
	rendered, err := Render(derived, overlay, resolver)
	if err != nil {
		return nil, err
	}
	return rendered, nil
}
