package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newOrchServer(t *testing.T) (*PeriscopeServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	s := &PeriscopeServer{clickhouse: db, logger: logging.NewLoggerWithService("orch-test")}
	return s, mock, func() { _ = db.Close() }
}

func TestListOrchestrators(t *testing.T) {
	s, mock, done := newOrchServer(t)
	defer done()

	const tenant = "tenant-1"
	seen := time.Unix(1_700_000_000, 0).UTC()
	mock.ExpectQuery(`SELECT tenant_id, orch_addr, last_seen, updated_at[\s\S]*FROM periscope\.orchestrator_state_current FINAL[\s\S]*WHERE tenant_id = \?[\s\S]*ORDER BY orch_addr ASC`).
		WithArgs(tenant).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "orch_addr", "last_seen", "updated_at"}).
			AddRow(tenant, "0xorchB", seen, seen).
			AddRow(tenant, "0xorchA", seen, seen))

	resp, err := s.ListOrchestrators(context.Background(), &periscopepb.ListOrchestratorsRequest{TenantId: tenant})
	if err != nil {
		t.Fatalf("ListOrchestrators: %v", err)
	}
	if len(resp.GetOrchestrators()) != 2 {
		t.Fatalf("got %d orchestrators, want 2", len(resp.GetOrchestrators()))
	}
	// Rows are surfaced in the order ClickHouse returns them (ORDER BY in SQL).
	if resp.GetOrchestrators()[0].GetOrchAddr() != "0xorchB" {
		t.Errorf("first orch = %q, want 0xorchB", resp.GetOrchestrators()[0].GetOrchAddr())
	}
	if resp.GetOrchestrators()[0].GetLastSeen().AsTime().Unix() != seen.Unix() {
		t.Errorf("last_seen not surfaced as timestamp")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListOrchestrators_RequiresTenant(t *testing.T) {
	s, _, done := newOrchServer(t)
	defer done()
	// No tenant in ctx or request → rejected before any query (no expectations).
	if _, err := s.ListOrchestrators(context.Background(), &periscopepb.ListOrchestratorsRequest{}); err == nil {
		t.Fatal("expected error when tenant is absent")
	}
}

// TestOrchestratorPerformanceSeriesMergesLegacyAndNetworkObservations pins
// the sources: legacy discovery rollups by owner, legacy outcomes by their
// cluster owner (tenant_id there is the stream tenant), and the gateway's
// schema-3 discovery and attempts from network_observations, all summed into
// one point per (bucket, gateway, region, IP).
func TestOrchestratorPerformanceSeriesMergesLegacyAndNetworkObservations(t *testing.T) {
	s, mock, done := newOrchServer(t)
	defer done()

	const (
		tenant = "tenant-1"
		orch   = "0xOrch"
		gw     = "gw-1"
		region = "eu-west"
		ip     = "10.0.0.1"
	)
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(time.Hour)
	ts := start
	sixColumns := []string{"ts", "gateway_id", "gateway_region", "resolved_ip", "attempts", "successes", "failures", "latency_sum", "latency_count", "max_latency"}

	mock.ExpectQuery(`FROM periscope\.orchestrator_discovery_5m\s+WHERE tenant_id = \?`).
		WithArgs(tenant, orch, start, end).
		WillReturnRows(sqlmock.NewRows(sixColumns).AddRow(ts, gw, region, ip, uint64(2), uint64(2), uint64(0), uint64(100), uint64(2), uint32(60)))
	mock.ExpectQuery(`FROM periscope\.orchestrator_transcode_outcomes\s+WHERE cluster_owner_tenant_id = \?`).
		WithArgs(tenant, orch, start, end).
		WillReturnRows(sqlmock.NewRows(append(sixColumns, "pixels")).
			AddRow(ts, gw, region, ip, uint64(10), uint64(8), uint64(2), uint64(800), uint64(8), uint32(150), uint64(123456)))
	mock.ExpectQuery(`FROM periscope\.orchestrator_ai_outcomes\s+WHERE cluster_owner_tenant_id = \?`).
		WithArgs(tenant, orch, start, end).
		WillReturnRows(sqlmock.NewRows(sixColumns).AddRow(ts, gw, region, ip, uint64(4), uint64(4), uint64(0), uint64(200), uint64(4), uint32(70)))
	networkColumns := []string{"ts", "gateway_id", "gateway_region", "resolved_ip", "kind", "attempts", "successes", "failures", "latency_sum", "latency_count", "max_latency", "pixels"}
	mock.ExpectQuery(`(?s)FROM periscope\.network_observations FINAL.*producer_kind = 'gateway'.*kind IN \('discovery', 'attempt'\) AND resource_id = lower\(\?\).*GROUP BY ts, gateway_id, gateway_region, resolved_ip, kind`).
		WithArgs(tenant, orch, start, sqlmock.AnyArg(), end).
		WillReturnRows(sqlmock.NewRows(networkColumns).
			AddRow(ts, gw, region, ip, "discovery", uint64(3), uint64(1), uint64(1), uint64(200), uint64(1), uint32(200), uint64(0)))
	// The window starts before the raw retention, so its older buckets come
	// from the gateway-keyed snapshots.
	mock.ExpectQuery(`(?s)FROM periscope\.network_gateway_rollups FINAL.*interval_seconds = \?.*resource_id = lower\(\?\).*bucket >= \? AND bucket < \? AND bucket <= \?.*GROUP BY ts, gateway_id, gateway_region, resolved_ip, kind`).
		WithArgs(tenant, uint32(300), orch, start, sqlmock.AnyArg(), end).
		WillReturnRows(sqlmock.NewRows(networkColumns).
			AddRow(ts, gw, region, ip, "attempt", uint64(5), uint64(5), uint64(0), uint64(1000), uint64(5), uint32(400), uint64(5000)))

	resp, err := s.GetOrchestratorPerformanceSeries(context.Background(), &periscopepb.GetOrchestratorPerformanceSeriesRequest{
		TenantId: tenant, OrchAddr: orch, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
	})
	if err != nil {
		t.Fatalf("GetOrchestratorPerformanceSeries: %v", err)
	}
	if len(resp.GetPoints()) != 1 {
		t.Fatalf("shared key should yield 1 point, got %d", len(resp.GetPoints()))
	}
	p := resp.GetPoints()[0]
	if p.GetAttempts() != 5 || p.GetSuccesses() != 3 || p.GetFailures() != 1 || p.GetMeanLatencyMs() != 100 || p.GetMaxLatencyMs() != 200 {
		t.Errorf("discovery = %d/%d/%d mean %v max %d, want 5/3/1 mean 100 max 200", p.GetAttempts(), p.GetSuccesses(), p.GetFailures(), p.GetMeanLatencyMs(), p.GetMaxLatencyMs())
	}
	if p.GetTranscodeAttempts() != 15 || p.GetTranscodeSuccesses() != 13 || p.GetTranscodeFailures() != 2 || p.GetTranscodePixels() != 128456 {
		t.Errorf("transcode counts wrong: %+v", p)
	}
	if p.GetTranscodeMeanOverallMs() != 138.46154 || p.GetTranscodeMaxOverallMs() != 400 { // 1800/13
		t.Errorf("transcode mean/max = %v/%d", p.GetTranscodeMeanOverallMs(), p.GetTranscodeMaxOverallMs())
	}
	if p.GetAiAttempts() != 4 || p.GetAiMeanLatencyMs() != 50 || p.GetAiMaxLatencyMs() != 70 {
		t.Errorf("ai fields wrong: %+v", p)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListOrchestratorVantages(t *testing.T) {
	s, mock, done := newOrchServer(t)
	defer done()

	const tenant = "tenant-1"
	now := time.Unix(1_700_000_000, 0).UTC()
	// CTE query references both source tables; tenant_id is bound in each CTE.
	mock.ExpectQuery(`(?s)orchestrator_vantage_current FINAL.*orchestrator_discovery_samples`).
		WithArgs(tenant, tenant).
		WillReturnRows(sqlmock.NewRows([]string{
			"tenant_id", "gateway_id", "gateway_region", "orch_addr", "resolved_ip",
			"latitude", "longitude", "city", "country_code", "geo_source", "geo_resolved_at",
			"latest_latency_ms", "score", "dialed_recently", "last_seen",
		}).AddRow(
			tenant, "gw-1", "eu-west", "0xorch", "10.0.0.1",
			52.37, 4.90, "Amsterdam", "NL", "maxmind", now,
			uint32(45), 0.95, uint8(1), now))

	resp, err := s.ListOrchestratorVantages(context.Background(), &periscopepb.ListOrchestratorVantagesRequest{TenantId: tenant})
	if err != nil {
		t.Fatalf("ListOrchestratorVantages: %v", err)
	}
	if len(resp.GetVantages()) != 1 {
		t.Fatalf("got %d vantages, want 1", len(resp.GetVantages()))
	}
	v := resp.GetVantages()[0]
	if v.GetCountryCode() != "NL" || v.GetLatestLatencyMs() != 45 {
		t.Errorf("vantage fields wrong: %+v", v)
	}
	if !v.GetDialedRecently() {
		t.Error("dialed_recently=1 should map to true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A vantage whose location was never resolved reads back ClickHouse's zero
// DateTime; it must surface as no resolution time, not the Unix epoch.
func TestListOrchestratorVantagesUnresolvedGeoHasNoResolvedAt(t *testing.T) {
	s, mock, done := newOrchServer(t)
	defer done()

	const tenant = "tenant-1"
	now := time.Unix(1_700_000_000, 0).UTC()
	mock.ExpectQuery(`(?s)orchestrator_vantage_current FINAL.*orchestrator_discovery_samples`).
		WithArgs(tenant, tenant).
		WillReturnRows(sqlmock.NewRows([]string{
			"tenant_id", "gateway_id", "gateway_region", "orch_addr", "resolved_ip",
			"latitude", "longitude", "city", "country_code", "geo_source", "geo_resolved_at",
			"latest_latency_ms", "score", "dialed_recently", "last_seen",
		}).AddRow(
			tenant, "gw-1", "eu-west", "0xorch", "10.0.0.1",
			0.0, 0.0, "", "", "unknown", time.Unix(0, 0).UTC(),
			uint32(45), 0.95, uint8(0), now))

	resp, err := s.ListOrchestratorVantages(context.Background(), &periscopepb.ListOrchestratorVantagesRequest{TenantId: tenant})
	if err != nil {
		t.Fatalf("ListOrchestratorVantages: %v", err)
	}
	if len(resp.GetVantages()) != 1 {
		t.Fatalf("got %d vantages, want 1", len(resp.GetVantages()))
	}
	if got := resp.GetVantages()[0].GetGeoResolvedAt(); got != nil {
		t.Fatalf("unresolved vantage reports geoResolvedAt %v, want none", got.AsTime())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
