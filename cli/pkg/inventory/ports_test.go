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
			"host-a": {Address: "10.0.0.1", User: "root"},
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
			"host-a": {Address: "10.0.0.1", User: "root"},
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
