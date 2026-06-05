package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
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

// TestMergeOrchestratorOutcomes pins the get-or-create accumulation: transcode
// and AI rows sharing the same (ts, gateway, ip) key merge into ONE point, with
// each side's fields populated and the mean computed from sum/count.
func TestMergeOrchestratorOutcomes(t *testing.T) {
	s, mock, done := newOrchServer(t)
	defer done()

	const (
		tenant = "tenant-1"
		orch   = "0xorch"
		gw     = "gw-1"
		region = "eu-west"
		ip     = "10.0.0.1"
	)
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(time.Hour)
	ts := start

	// Transcode (5m branch): 11 columns.
	mock.ExpectQuery(`FROM periscope\.orchestrator_transcode_outcomes`).
		WithArgs(tenant, orch, start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"ts", "gateway_id", "gateway_region", "resolved_ip",
			"attempts", "successes", "failures",
			"overall_ms_sum", "overall_ms_count", "max_overall_ms", "pixels",
		}).AddRow(ts, gw, region, ip,
			uint64(10), uint64(8), uint64(2),
			uint64(800), uint64(8), uint32(150), uint64(123456)))

	// AI (5m branch): 10 columns, same key → merges into the same point.
	mock.ExpectQuery(`FROM periscope\.orchestrator_ai_outcomes`).
		WithArgs(tenant, orch, start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"ts", "gateway_id", "gateway_region", "resolved_ip",
			"attempts", "successes", "failures",
			"latency_ms_sum", "latency_ms_count", "max_latency_ms",
		}).AddRow(ts, gw, region, ip,
			uint64(4), uint64(4), uint64(0),
			uint64(200), uint64(4), uint32(70)))

	points := map[orchestratorPerformanceKey]*periscopepb.OrchestratorPerformancePoint{}
	if err := s.mergeOrchestratorTranscodeOutcomes(context.Background(), points, tenant, orch, start, end, "5m", "", ""); err != nil {
		t.Fatalf("transcode merge: %v", err)
	}
	if err := s.mergeOrchestratorAIOutcomes(context.Background(), points, tenant, orch, start, end, "5m", "", ""); err != nil {
		t.Fatalf("ai merge: %v", err)
	}

	if len(points) != 1 {
		t.Fatalf("shared key should yield 1 point, got %d", len(points))
	}
	var p *periscopepb.OrchestratorPerformancePoint
	for _, v := range points {
		p = v
	}
	if p.GetTranscodeAttempts() != 10 || p.GetTranscodeSuccesses() != 8 || p.GetTranscodeFailures() != 2 {
		t.Errorf("transcode counts wrong: %+v", p)
	}
	if p.GetTranscodeMeanOverallMs() != 100 { // 800/8
		t.Errorf("transcode mean = %v, want 100", p.GetTranscodeMeanOverallMs())
	}
	if p.GetTranscodeMaxOverallMs() != 150 || p.GetTranscodePixels() != 123456 {
		t.Errorf("transcode max/pixels wrong: %+v", p)
	}
	if p.GetAiAttempts() != 4 || p.GetAiMeanLatencyMs() != 50 { // 200/4
		t.Errorf("ai fields wrong: attempts=%d mean=%v", p.GetAiAttempts(), p.GetAiMeanLatencyMs())
	}
	if p.GetAiMaxLatencyMs() != 70 {
		t.Errorf("ai max latency = %d, want 70", p.GetAiMaxLatencyMs())
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
