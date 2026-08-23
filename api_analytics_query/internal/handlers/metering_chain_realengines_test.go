//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type capturedUsageProducer struct {
	reports []models.UsageSummary
	fail    bool
}

func (p *capturedUsageProducer) ProduceMessage(_ string, _ []byte, value []byte, _ map[string]string) error {
	if p.fail {
		return errors.New("injected publication failure")
	}
	var report models.UsageSummary
	if err := json.Unmarshal(value, &report); err != nil {
		return err
	}
	p.reports = append(p.reports, report)
	return nil
}

func TestCrossEngineMeteringReplayLateCorrectionAndFencing_RealEngines(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	ch := dockerch.StartCurrent(t, root, "fw-metering-chain-query").SQL
	pg := startMeteringChainPostgres(t)
	queries := meteringdb.New(pg)
	producer := &capturedUsageProducer{}
	bs := &BillingSummarizer{
		postgresQueries: queries, clickhouse: ch, logger: logging.NewLogger(), usageProducer: producer,
		resolvePrimaryCluster: func(string) (string, error) { return "cluster-chain-1", nil },
		billingTopic:          "billing.usage_reports", sourceID: "chain-source", sourceRegion: "test", systemTenantID: uuid.NewString(),
	}
	ctx := context.Background()
	tenantID, streamID := uuid.NewString(), uuid.NewString()
	windowStart := time.Now().UTC().Add(-15 * time.Minute).Truncate(5 * time.Minute)
	windowEnd := windowStart.Add(5 * time.Minute)
	projectionMS := windowStart.Add(time.Minute).UnixMilli()
	lateEndedMS := windowStart.Add(-2 * time.Hour).UnixMilli()

	viewerInsert := `INSERT INTO periscope.viewer_sessions_final
		(tenant_id,node_id,session_id,source_event_id,cluster_id,stream_id,duration_seconds,uploaded_bytes,downloaded_bytes,source_started_at_ms,source_ended_at_ms,edge_received_at_ms,projection_version_ms,closed_reason,payload_raw)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	viewerArgs := []any{tenantID, "edge-chain-1", "viewer-late", "viewer-source-1", "cluster-chain-1", streamID,
		uint32(600), uint64(1 << 30), uint64(2 << 30), lateEndedMS - 600_000, lateEndedMS, lateEndedMS, projectionMS, "final", "{}"}
	if _, err := ch.ExecContext(ctx, viewerInsert, viewerArgs...); err != nil {
		t.Fatal(err)
	}
	viewerArgs[12] = projectionMS + 10_000
	if _, err := ch.ExecContext(ctx, viewerInsert, viewerArgs...); err != nil {
		t.Fatalf("duplicate viewer projection: %v", err)
	}

	streamInsert := `INSERT INTO periscope.stream_sessions_final
		(tenant_id,node_id,stream_id,source_event_id,cluster_id,total_viewers,source_started_at_ms,source_ended_at_ms,edge_received_at_ms,projection_version_ms,closed_reason,payload_raw)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	if _, err := ch.ExecContext(ctx, streamInsert, tenantID, "edge-chain-1", streamID, "stream-source-1", "cluster-chain-1", int64(7),
		lateEndedMS-900_000, lateEndedMS, lateEndedMS, projectionMS+20_000, "final", "{}"); err != nil {
		t.Fatal(err)
	}
	apiInsert := `INSERT INTO periscope.api_requests
		(timestamp,tenant_id,source_node,source_event_id,ingested_at_ms,auth_type,operation_name,operation_type,request_count,error_count,total_duration_ms,total_complexity,user_hashes,token_hashes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	apiArgs := []any{time.UnixMilli(lateEndedMS), tenantID, "skipper", "api-source-late:0", projectionMS + 25_000,
		"service", "Search", "skipper_search_query", uint32(3), uint32(1), uint64(120), uint32(9), []uint64{11, 12}, []uint64{21}}
	if _, err := ch.ExecContext(ctx, apiInsert, apiArgs...); err != nil {
		t.Fatal(err)
	}
	apiArgs[4] = projectionMS + 26_000
	if _, err := ch.ExecContext(ctx, apiInsert, apiArgs...); err != nil {
		t.Fatalf("duplicate API projection: %v", err)
	}

	correctedSession := "viewer-corrected"
	priorProjectionMS := windowStart.Add(-5*time.Minute + time.Minute).UnixMilli()
	priorArgs := []any{tenantID, "edge-chain-1", correctedSession, "viewer-source-correction", "cluster-chain-1", streamID,
		uint32(300), uint64(0), uint64(0), lateEndedMS - 300_000, lateEndedMS, lateEndedMS, priorProjectionMS, "final", "{}"}
	if _, err := ch.ExecContext(ctx, viewerInsert, priorArgs...); err != nil {
		t.Fatal(err)
	}
	priorArgs[6] = uint32(420)
	priorArgs[9] = lateEndedMS - 420_000
	priorArgs[12] = projectionMS + 30_000
	if _, err := ch.ExecContext(ctx, viewerInsert, priorArgs...); err != nil {
		t.Fatal(err)
	}
	naturalKey, _ := json.Marshal(map[string]string{"tenant_id": tenantID, "node_id": "edge-chain-1", "session_id": correctedSession, "cluster_id": "cluster-chain-1"})
	if _, err := ch.ExecContext(ctx, `INSERT INTO periscope.projection_divergences
		(observed_at_ms,table_name,meter,field,natural_key_json,prior_value_json,new_value_json,source_event_id)
		VALUES (?,?,?,?,?,?,?,?)`, projectionMS+30_000, "viewer_sessions_final", "delivered_minutes", "duration_seconds", string(naturalKey), "300", "420", "viewer-source-correction"); err != nil {
		t.Fatal(err)
	}

	if err := queries.InitializeBillingCursor(ctx, meteringdb.InitializeBillingCursorParams{SourceID: bs.sourceID, TenantID: tenantID, LastProcessedAt: windowStart}); err != nil {
		t.Fatal(err)
	}
	producer.fail = true
	if err := bs.processBillingSlice(ctx, tenantID, windowStart, windowEnd); err == nil {
		t.Fatal("publication failure must fail the billing slice")
	}
	assertMeteringCursor(t, queries, bs.sourceID, tenantID, windowStart)
	producer.fail = false
	if err := bs.processBillingSlice(ctx, tenantID, windowStart, windowEnd); err != nil {
		t.Fatalf("billing slice: %v", err)
	}
	assertMeteringCursor(t, queries, bs.sourceID, tenantID, windowEnd)
	if len(producer.reports) != 1 {
		t.Fatalf("published reports=%d, want 1", len(producer.reports))
	}
	first := producer.reports[0]
	assertMeter(t, first, "delivered_minutes", 10)
	assertMeter(t, first, "egress_gb", 2)
	assertMeter(t, first, "stream_runtime_seconds", 900)
	assertMeter(t, first, "api_requests", 3)
	assertMeter(t, first, "api_errors", 1)
	if len(first.UsageAdjustments) != 1 || first.UsageAdjustments[0].DeltaValue != 2 {
		t.Fatalf("correction adjustments=%#v, want +2 delivered minutes", first.UsageAdjustments)
	}

	if _, err := pg.ExecContext(ctx, `UPDATE periscope.billing_cursors SET last_processed_at = $1 WHERE source_id = $2 AND tenant_id = $3`, windowStart, bs.sourceID, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := bs.processBillingSlice(ctx, tenantID, windowStart, windowEnd); err != nil {
		t.Fatalf("replayed billing slice: %v", err)
	}
	if len(producer.reports) != 2 || producer.reports[1].ReportID != first.ReportID {
		t.Fatalf("replay report identity changed: %#v", producer.reports)
	}
	assertMeter(t, producer.reports[1], "delivered_minutes", 10)
	assertMeter(t, producer.reports[1], "stream_runtime_seconds", 900)
	assertMeter(t, producer.reports[1], "api_requests", 3)

	testReservationLifecycle(t, ctx, ch, queries, bs, producer, tenantID, streamID)
}

func testReservationLifecycle(t *testing.T, ctx context.Context, ch *sql.DB, queries *meteringdb.Queries, bs *BillingSummarizer, producer *capturedUsageProducer, tenantID, streamID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	insert := `INSERT INTO periscope.viewer_connection_events
		(event_id,timestamp,tenant_id,stream_id,internal_name,session_id,connection_addr,connector,node_id,cluster_id,country_code,city,latitude,longitude,event_type,session_duration,bytes_transferred)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if _, err := ch.ExecContext(ctx, insert, uuid.NewString(), now.Add(-2*time.Minute), tenantID, streamID, "live+chain", "open-chain", "203.0.113.1", "hls", "edge-chain-1", "cluster-chain-1", "US", "Test", 0.0, 0.0, "connect", uint32(0), uint64(0)); err != nil {
		t.Fatal(err)
	}
	before := len(producer.reports)
	if err := bs.PublishUsageReservations(ctx); err != nil {
		t.Fatalf("publish reservation: %v", err)
	}
	keys, err := queries.ListReservationKeys(ctx, bs.sourceID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("reservation keys=%#v err=%v", keys, err)
	}
	if len(producer.reports) != before+1 || producer.reports[before].ReportKind != "reservation" {
		t.Fatalf("reservation publication missing: %#v", producer.reports[before:])
	}
	if _, err := ch.ExecContext(ctx, insert, uuid.NewString(), now, tenantID, streamID, "live+chain", "open-chain", "203.0.113.1", "hls", "edge-chain-1", "cluster-chain-1", "US", "Test", 0.0, 0.0, "disconnect", uint32(120), uint64(0)); err != nil {
		t.Fatal(err)
	}
	if err := bs.PublishUsageReservations(ctx); err != nil {
		t.Fatalf("release reservation: %v", err)
	}
	keys, err = queries.ListReservationKeys(ctx, bs.sourceID)
	if err != nil || len(keys) != 0 {
		t.Fatalf("released reservation keys=%#v err=%v", keys, err)
	}
}

func assertMeteringCursor(t *testing.T, queries *meteringdb.Queries, sourceID, tenantID string, want time.Time) {
	t.Helper()
	got, err := queries.GetBillingCursor(context.Background(), meteringdb.GetBillingCursorParams{SourceID: sourceID, TenantID: tenantID})
	if err != nil || !got.Equal(want) {
		t.Fatalf("cursor=%v err=%v, want %v", got, err, want)
	}
}

func assertMeter(t *testing.T, report models.UsageSummary, meter string, want float64) {
	t.Helper()
	for _, quantity := range report.Meters {
		if quantity.Meter == meter {
			if quantity.Quantity != want {
				t.Fatalf("meter %s=%v, want %v", meter, quantity.Quantity, want)
			}
			return
		}
	}
	t.Fatalf("meter %s absent from %#v", meter, report.Meters)
}

func startMeteringChainPostgres(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-metering-chain-pg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/periscope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
