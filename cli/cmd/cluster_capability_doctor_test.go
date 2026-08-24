package cmd

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestDoctorCapabilitiesSkipDisabledEngines(t *testing.T) {
	manifest := &inventory.Manifest{Infrastructure: inventory.InfrastructureConfig{
		Postgres:   &inventory.PostgresConfig{},
		ClickHouse: &inventory.ClickHouseConfig{},
	}}
	postgres := doctorPostgresCapabilities(context.Background(), nil, manifest, inventory.Host{}, "")
	if !postgres.OK || postgres.Status != "healthy" {
		t.Fatalf("disabled Postgres result = %+v", postgres)
	}
	clickhouse := doctorClickHouseCapabilities(context.Background(), nil, manifest, nil)
	if !clickhouse.OK || clickhouse.Status != "healthy" {
		t.Fatalf("disabled ClickHouse result = %+v", clickhouse)
	}
}
