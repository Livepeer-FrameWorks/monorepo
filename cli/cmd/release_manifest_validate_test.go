package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReleaseManifestCommand(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(valid, []byte("platform_version: v0.3.0\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newValidateReleaseManifestCmd()
	cmd.SetArgs([]string{valid})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate release manifest: %v", err)
	}
}

func TestValidateReleaseManifestCommandRejectsGeneratedSchemaDrift(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "manifest.yaml")
	data := "platform_version: v0.3.0\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure:\n  - name: yugabyte\n    version: test\n    contract_image: test\n"
	if err := os.WriteFile(invalid, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newValidateReleaseManifestCmd()
	cmd.SetArgs([]string{invalid})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "field contract_image not found") {
		t.Fatalf("expected strict schema error, got %v", err)
	}
}
