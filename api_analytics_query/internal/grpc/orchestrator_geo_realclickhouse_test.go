//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestOrchestratorGeoFallback_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t, "orchestrator_vantage_current", "orchestrator_discovery_samples")
	server := NewPeriscopeServer(db, logging.NewLogger())
	tenant := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.orchestrator_vantage_current
	(tenant_id,gateway_id,gateway_region,orch_addr,resolved_ip,latitude,longitude,geo_source,geo_resolved_at,last_seen,updated_at)
	VALUES (?,'gateway','eu','0xgeo','192.0.2.1',52,4,'mmdb',?,?,?)`, tenant, now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, nulls := range []string{"0", "1"} {
		query, args := buildOrchestratorVantagesQuery(tenant, "0xgeo")
		rows, err := db.QueryContext(t.Context(), query+" SETTINGS join_use_nulls="+nulls, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			rows.Close()
			t.Fatal("missing vantage")
		}
		values := make([]any, 15)
		for i := range values {
			values[i] = new(any)
		}
		if err := rows.Scan(values...); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if lat, lon := *values[5].(*any), *values[6].(*any); lat != float64(52) || lon != float64(4) {
			t.Fatalf("join_use_nulls=%s erased current location: %v,%v, want 52,4", nulls, lat, lon)
		}
	}
	got, err := server.queryOrchestratorVantages(t.Context(), tenant, "0xgeo")
	if err != nil || len(got) != 1 || got[0].GeoSource != "mmdb" {
		t.Fatalf("fallback lost source: %v %v", got, err)
	}
}

func TestOrchestratorGeoHistoryIsolation_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t, "orchestrator_vantage_current", "orchestrator_discovery_samples")
	server := NewPeriscopeServer(db, logging.NewLogger())
	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	for _, current := range []struct {
		gateway, ip, source string
		latitude, longitude float64
	}{
		{"unknown", "192.0.2.1", "unknown", 0, 0},
		{"newer", "192.0.2.1", "mmdb", 52, 4},
		{"isolated", "192.0.2.1", "unknown", 0, 0},
		{"equator", "192.0.2.1", "configured", 0, 4},
	} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.orchestrator_vantage_current
   (tenant_id,gateway_id,orch_addr,resolved_ip,latitude,longitude,geo_source,geo_resolved_at,last_seen,updated_at)
   VALUES (?,?,'0xgeo',?,?,?,?,?,?,?)`, tenant, current.gateway, current.ip, current.latitude, current.longitude, current.source, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, history := range []struct{ tenant, gateway, ip string }{
		{tenant, "unknown", "192.0.2.1"}, {tenant, "newer", "192.0.2.1"},
		{otherTenant, "isolated", "192.0.2.1"}, {tenant, "isolated", "192.0.2.2"},
	} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.orchestrator_discovery_samples
   (tenant_id,gateway_id,orch_addr,resolved_ip,timestamp,latitude,longitude,geo_source,country_code)
   VALUES (?,?,'0xgeo',?,?,12,13,'mmdb','IN')`, history.tenant, history.gateway, history.ip, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := server.queryOrchestratorVantages(t.Context(), tenant, "0xgeo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("tenant isolation failed: %d vantages", len(got))
	}
	for _, v := range got {
		switch v.GatewayId {
		case "unknown":
			if v.Latitude != 12 || v.Longitude != 13 || v.CountryCode != "IN" || v.GeoSource != "mmdb" {
				t.Fatalf("history fallback lost coherent facts: %+v", v)
			}
		case "newer":
			if v.Latitude != 52 || v.Longitude != 4 {
				t.Fatalf("old history replaced newer current geo: %+v", v)
			}
		case "isolated":
			if v.Latitude != 0 || v.Longitude != 0 {
				t.Fatalf("geo leaked across tenant/gateway/IP: %+v", v)
			}
		case "equator":
			if v.Latitude != 0 || v.Longitude != 4 {
				t.Fatalf("real zero latitude was discarded: %+v", v)
			}
		default:
			t.Fatalf("unexpected gateway %+v", v)
		}
	}
}
