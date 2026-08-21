//go:build schema_verify

package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type harnessInfrastructure struct {
	Name           string `yaml:"name"`
	Image          string `yaml:"image"`
	Digest         string `yaml:"digest"`
	ContractImage  string `yaml:"contract_image"`
	ContractDigest string `yaml:"contract_digest"`
}

func infrastructureHarnessImage(t *testing.T, name string) string {
	t.Helper()
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found; real-engine tests require the release image authority")
	}

	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	var doc struct {
		Infrastructure []harnessInfrastructure `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", manifestPath, err)
	}

	for _, infra := range doc.Infrastructure {
		if infra.Name != name {
			continue
		}
		if strings.TrimSpace(infra.Image) == "" || strings.TrimSpace(infra.Digest) == "" {
			t.Fatalf("infrastructure/%s must declare both image and digest", name)
		}
		return infra.Image + "@" + infra.Digest
	}
	t.Fatalf("infrastructure/%s is absent from %s", name, manifestPath)
	return ""
}

func infrastructureContractImage(t *testing.T, name string) string {
	t.Helper()
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found; compatibility tests require the engine authority")
	}
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	var doc struct {
		Infrastructure []harnessInfrastructure `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", manifestPath, err)
	}
	for _, infra := range doc.Infrastructure {
		if infra.Name != name {
			continue
		}
		if infra.ContractImage == "" || infra.ContractDigest == "" {
			t.Fatalf("infrastructure/%s must pin contract_image and contract_digest", name)
		}
		return infra.ContractImage + "@" + infra.ContractDigest
	}
	t.Fatalf("infrastructure/%s is absent from %s", name, manifestPath)
	return ""
}

func TestComposeUsesSchemaHarnessImages(t *testing.T) {
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found")
	}
	composePath := filepath.Join(filepath.Dir(filepath.Dir(manifestPath)), "docker-compose.yml")
	b, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read %s: %v", composePath, err)
	}
	compose := string(b)
	for _, name := range []string{"postgresql", "clickhouse"} {
		image := infrastructureHarnessImage(t, name)
		if !strings.Contains(compose, "image: "+image) {
			t.Errorf("docker-compose.yml does not use release-authority image %q for %s", image, name)
		}
	}
}
