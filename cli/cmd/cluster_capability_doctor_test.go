package cmd

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func TestDoctorCapabilitiesSkipDisabledEngines(t *testing.T) {
	manifest := &inventory.Manifest{Infrastructure: inventory.InfrastructureConfig{
		Postgres:   &inventory.PostgresConfig{},
		ClickHouse: &inventory.ClickHouseConfig{},
	}}
	postgres := doctorPostgresCapabilities(context.Background(), nil, manifest, inventory.Host{}, "", "v0.3.10")
	if !postgres.OK || postgres.Status != "healthy" {
		t.Fatalf("disabled Postgres result = %+v", postgres)
	}
	clickhouse := doctorClickHouseCapabilities(context.Background(), nil, manifest, nil, "v0.3.10")
	if !clickhouse.OK || clickhouse.Status != "healthy" {
		t.Fatalf("disabled ClickHouse result = %+v", clickhouse)
	}
}

func TestDoctorCapabilitiesRespectTargetRelease(t *testing.T) {
	t.Parallel()

	before := doctorCapabilitiesForVersion("periscope-ingest", pkgdatabase.EngineClickHouse, "v0.3.10")
	if len(before) != 2 {
		t.Fatalf("v0.3.10 ClickHouse capabilities = %d, want 2", len(before))
	}
	current := doctorCapabilitiesForVersion("periscope-ingest", pkgdatabase.EngineClickHouse, "v0.3.11")
	if len(current) != 3 || current[2].Name != "domain event projections" {
		t.Fatalf("v0.3.11 ClickHouse capabilities = %#v, want domain event projections included", current)
	}
}
