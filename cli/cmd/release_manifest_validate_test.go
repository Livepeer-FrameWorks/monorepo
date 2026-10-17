package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseManifestFromInfrastructure assembles a manifest the way the release
// workflow does: a generated header followed by config/infrastructure.yaml
// appended verbatim.
func releaseManifestFromInfrastructure(t *testing.T, infra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	data := "platform_version: v0.3.0\nservices: []\nnative_binaries: []\ninterfaces: []\n\n" + infra
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func repoInfrastructureYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "infrastructure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestValidateReleaseManifestCommand(t *testing.T) {
	cmd := newValidateReleaseManifestCmd()
	cmd.SetArgs([]string{releaseManifestFromInfrastructure(t, repoInfrastructureYAML(t))})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate release manifest: %v", err)
	}
}

func TestValidateReleaseManifestCommandRequiresProvisionedExporters(t *testing.T) {
	full := repoInfrastructureYAML(t)
	for _, name := range []string{"node-exporter", "jmx-prometheus-javaagent", "redis-exporter", "postgres-exporter"} {
		t.Run(name, func(t *testing.T) {
			start := strings.Index(full, "  - name: "+name+"\n")
			if start < 0 {
				t.Fatalf("config/infrastructure.yaml has no %s entry", name)
			}
			end := strings.Index(full[start+1:], "  - name: ")
			if end < 0 {
				t.Fatalf("%s is the last entry; fixture cannot cut it", name)
			}
			without := full[:start] + full[start+1+end:]
			cmd := newValidateReleaseManifestCmd()
			cmd.SetArgs([]string{releaseManifestFromInfrastructure(t, without)})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), `infrastructure entry "`+name+`" is missing`) {
				t.Fatalf("expected missing %s to fail validation, got %v", name, err)
			}
		})
	}
}

func TestValidateReleaseManifestCommandRequiresChecksums(t *testing.T) {
	infra := strings.Replace(repoInfrastructureYAML(t),
		"        checksum: sha256:b51d8a76aa2a9156a55d501aca6276fae09e262259a5e4e831d2c2222f084e63\n", "", 1)
	cmd := newValidateReleaseManifestCmd()
	cmd.SetArgs([]string{releaseManifestFromInfrastructure(t, infra)})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "node-exporter linux-amd64 artifact has no checksum") {
		t.Fatalf("expected missing node-exporter checksum to fail validation, got %v", err)
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
