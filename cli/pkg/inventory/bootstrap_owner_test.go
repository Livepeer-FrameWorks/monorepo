package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestValidateClusterOwnerTenantUsesBootstrapAlias(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", Cluster: "tenant-edge"},
		},
		Clusters: map[string]ClusterConfig{
			"tenant-edge": {
				Name:        "Tenant Edge",
				Type:        "edge",
				OwnerTenant: "northwind",
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("bootstrap tenant alias should be valid: %v", err)
	}
}

func TestManifestValidateClusterOwnerTenantRejectsUUID(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", Cluster: "tenant-edge"},
		},
		Clusters: map[string]ClusterConfig{
			"tenant-edge": {
				Name:        "Tenant Edge",
				Type:        "edge",
				OwnerTenant: "5eed517e-ba5e-da7a-517e-ba5eda7a0001",
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected UUID owner_tenant to fail validation")
	}
	if !strings.Contains(err.Error(), "bootstrap tenant alias") {
		t.Fatalf("expected bootstrap alias error, got %v", err)
	}
}

func TestLoadRejectsSharedRegistryOwnerBeforeReadingHosts(t *testing.T) {
	root := t.TempDir()
	var target string
	for _, name := range []string{"production", "staging"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "cluster.yaml")
		if err := os.WriteFile(path, []byte("version: v1\ntype: cluster\nregistry_owner: shared-owner\nhosts_file: must-not-be-read.enc.yaml\n"), 0600); err != nil {
			t.Fatal(err)
		}
		target = path
	}
	if _, err := LoadWithHosts(target, ""); err == nil || !strings.Contains(err.Error(), "each manifest requires its own owner") {
		t.Fatalf("duplicate manifest ownership accepted or secret inventory read first: %v", err)
	}
	if err := validateRegistryOwnerFile(target, "distinct-owner"); err != nil {
		t.Fatalf("distinct owner rejected: %v", err)
	}
}
