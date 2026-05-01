package inventory

import (
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
