package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCapabilityCatalogServices(t *testing.T) {
	want := []string{
		"commodore",
		"foghorn",
		"navigator",
		"periscope-ingest",
		"periscope-metering",
		"periscope-query",
		"purser",
		"quartermaster",
		"skipper",
	}
	if got := CapabilityServices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CapabilityServices() = %v, want %v", got, want)
	}
	for _, service := range want {
		if got := len(capabilityCatalog[service]); got == 0 {
			t.Errorf("%s has no capability probes", service)
		}
	}
}

func TestCapabilitiesForReturnsCopyForEngine(t *testing.T) {
	postgres := CapabilitiesFor("periscope-metering", EnginePostgres)
	clickhouse := CapabilitiesFor("periscope-metering", EngineClickHouse)
	if len(postgres) != 2 || len(clickhouse) != 2 {
		t.Fatalf("periscope-metering capabilities = postgres:%d clickhouse:%d, want 2 each", len(postgres), len(clickhouse))
	}
	postgres[0].Probe = "broken"
	if capabilityCatalog["periscope-metering"][0].Probe == "broken" {
		t.Fatal("CapabilitiesFor returned catalog storage instead of a copy")
	}
}

func TestNavigatorCapabilitiesCoverAliasWorkQueues(t *testing.T) {
	capabilities := CapabilitiesFor("navigator", EnginePostgres)
	if len(capabilities) != 2 {
		t.Fatalf("Navigator capabilities = %d, want edge state and retirement queue", len(capabilities))
	}
	if capabilities[0].Name != "tenant edge apply state" || capabilities[1].Name != "tenant alias retirement queue" {
		t.Fatalf("Navigator capability names = %q, %q", capabilities[0].Name, capabilities[1].Name)
	}
}

func TestVerifyCapabilitiesIdentifiesFailedRequirement(t *testing.T) {
	sentinel := errors.New("undefined column")
	queries := 0
	err := VerifyCapabilities(context.Background(), "purser", EnginePostgres, func(_ context.Context, query string) error {
		queries++
		if queries == 2 {
			return sentinel
		}
		if query == "" {
			t.Fatal("empty capability probe")
		}
		return nil
	})
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("VerifyCapabilities() error = %T %v, want *CapabilityError", err, err)
	}
	if capabilityErr.Service != "purser" || capabilityErr.Engine != EnginePostgres || capabilityErr.Capability != "provider webhook leasing" {
		t.Fatalf("capability error = %+v", capabilityErr)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("capability error does not unwrap original error: %v", err)
	}
	if queries != 2 {
		t.Fatalf("executed %d probes after failure, want 2", queries)
	}
}
