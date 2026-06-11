package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func analyticsTestManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"control-1": {WireguardIP: "10.66.0.10"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Host:    "control-1",
				Port:    5432,
				Databases: []inventory.DatabaseConfig{
					{Name: "quartermaster"},
					{Name: "purser"},
					{Name: "navigator"}, // no analytics seed, no collection
				},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Host:    "analytics-1", // split from postgres, uses mesh address
			},
		},
	}
}

func TestBuildAnalyticsNamedCollectionsPerServiceDatabase(t *testing.T) {
	manifest := analyticsTestManifest()
	collections := buildAnalyticsNamedCollections(manifest, map[string]string{
		"ANALYTICS_RO_PASSWORD": "secret",
	})

	if len(collections) != 2 {
		t.Fatalf("expected collections for quartermaster+purser only, got %d: %#v", len(collections), collections)
	}
	first := collections[0]
	if first["name"] != "quartermaster_pg" {
		t.Fatalf("expected quartermaster_pg first, got %v", first["name"])
	}
	settings, ok := first["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing: %#v", first)
	}
	if settings["host"] != "10.66.0.10" {
		t.Fatalf("expected mesh address for postgres host, got %v", settings["host"])
	}
	if settings["database"] != "quartermaster" || settings["schema"] != "quartermaster" {
		t.Fatalf("database/schema must both be the service schema, got %#v", settings)
	}
	if settings["user"] != "frameworks_analytics_ro" || settings["password"] != "secret" {
		t.Fatalf("credentials mismatch: %#v", settings)
	}
}

// Colocated ClickHouse+Postgres must connect over loopback: the default
// pg_hba allows loopback and docker-bridge only, never the mesh CIDR.
func TestBuildAnalyticsNamedCollectionsUsesLoopbackWhenColocated(t *testing.T) {
	manifest := analyticsTestManifest()
	manifest.Infrastructure.ClickHouse.Host = "control-1"

	collections := buildAnalyticsNamedCollections(manifest, map[string]string{
		"ANALYTICS_RO_PASSWORD": "secret",
	})
	if len(collections) == 0 {
		t.Fatal("expected collections")
	}
	settings := collections[0]["settings"].(map[string]any)
	if settings["host"] != "127.0.0.1" {
		t.Fatalf("expected loopback for colocated clickhouse+postgres, got %v", settings["host"])
	}
}

func TestBuildAnalyticsNamedCollectionsRequiresPassword(t *testing.T) {
	manifest := analyticsTestManifest()
	if got := buildAnalyticsNamedCollections(manifest, map[string]string{}); got != nil {
		t.Fatalf("expected nil without ANALYTICS_RO_PASSWORD, got %#v", got)
	}
	manifest.Infrastructure.Postgres.Enabled = false
	if got := buildAnalyticsNamedCollections(manifest, map[string]string{"ANALYTICS_RO_PASSWORD": "x"}); got != nil {
		t.Fatalf("expected nil when postgres disabled, got %#v", got)
	}
}
