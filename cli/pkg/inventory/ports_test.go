package inventory

import (
	"strings"
	"testing"
)

func TestManifestValidateDetectsPortCollisions(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-a": {ExternalIP: "10.0.0.1", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Postgres: &PostgresConfig{
				Enabled: true,
				Host:    "host-a",
				Port:    5432,
			},
		},
		Services: map[string]ServiceConfig{
			"bridge": {
				Enabled: true,
				Host:    "host-a",
				Port:    5432,
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected port collision error, got nil")
	}
	if !strings.Contains(err.Error(), "port 5432") {
		t.Fatalf("expected port collision error, got: %v", err)
	}
}

func TestManifestValidateDetectsGRPCPortCollisions(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-a": {ExternalIP: "10.0.0.1", User: "root"},
		},
		Services: map[string]ServiceConfig{
			"quartermaster": {
				Enabled: true,
				Host:    "host-a",
			},
			"bridge": {
				Enabled: true,
				Host:    "host-a",
				Port:    19002,
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected gRPC port collision error, got nil")
	}
	if !strings.Contains(err.Error(), "19002") {
		t.Fatalf("expected gRPC port collision error, got: %v", err)
	}
}

func TestManifestValidateDetectsYugabyteTServerWebPortCollisions(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-a": {ExternalIP: "10.0.0.1", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Postgres: &PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Mode:    "native",
				Version: "2025.1.3.2",
				Nodes: []PostgresNode{
					{Host: "host-a", ID: 1},
				},
			},
		},
		Services: map[string]ServiceConfig{
			"bridge": {
				Enabled: true,
				Host:    "host-a",
				Port:    11000,
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected Yugabyte web port collision error, got nil")
	}
	if !strings.Contains(err.Error(), "11000") {
		t.Fatalf("expected Yugabyte web port collision error, got: %v", err)
	}
}

func TestManifestValidateDetectsYugabyteDefaultYSQLPortCollisions(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-a": {ExternalIP: "10.0.0.1", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Postgres: &PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Mode:    "native",
				Version: "2025.1.3.2",
				Nodes: []PostgresNode{
					{Host: "host-a", ID: 1},
				},
			},
		},
		Services: map[string]ServiceConfig{
			"bridge": {
				Enabled: true,
				Host:    "host-a",
				Port:    5433,
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected Yugabyte YSQL port collision error, got nil")
	}
	if !strings.Contains(err.Error(), "5433") {
		t.Fatalf("expected Yugabyte YSQL port collision error, got: %v", err)
	}
}

func TestManifestValidateDetectsClickHouseAuxiliaryPortCollisions(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-a": {ExternalIP: "10.0.0.1", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			ClickHouse: &ClickHouseConfig{
				Enabled: true,
				Mode:    "native",
				Version: "25.9.2.1",
				Host:    "host-a",
			},
		},
		Services: map[string]ServiceConfig{
			"bridge": {
				Enabled: true,
				Host:    "host-a",
				Port:    8123,
			},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected ClickHouse HTTP port collision error, got nil")
	}
	if !strings.Contains(err.Error(), "8123") {
		t.Fatalf("expected ClickHouse HTTP port collision error, got: %v", err)
	}
}
