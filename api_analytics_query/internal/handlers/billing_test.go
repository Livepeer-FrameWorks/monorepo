package handlers

import (
	"context"
	"math"
	"testing"
	"time"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSanitizeFloat(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{name: "normal", input: 12.5, expected: 12.5},
		{name: "nan", input: math.NaN(), expected: 0},
		{name: "inf", input: math.Inf(1), expected: 0},
		{name: "neg_inf", input: math.Inf(-1), expected: 0},
		{name: "small", input: 1e-9, expected: 1e-9},
		{name: "large", input: 9e15, expected: 9e15},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := sanitizeFloat(test.input)
			if math.IsNaN(test.input) || math.IsInf(test.input, 0) {
				if actual != 0 {
					t.Fatalf("expected 0, got %v", actual)
				}
				return
			}
			if actual != test.expected {
				t.Fatalf("expected %v, got %v", test.expected, actual)
			}
		})
	}
}

func TestAlignBillingCursorStartFloorsLegacyCursor(t *testing.T) {
	got := alignBillingCursorStart(time.Date(2026, 5, 25, 19, 52, 23, 0, time.UTC), 5*time.Minute)
	want := time.Date(2026, 5, 25, 19, 50, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("aligned cursor = %s, want %s", got, want)
	}
}

func TestEarliestCanonicalBillingFactCastsAPIWindowStartFromDateTime(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	bs := &BillingSummarizer{clickhouse: db, logger: logging.NewLogger()}
	first := time.Date(2026, 5, 27, 10, 15, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT min\(first_ms\).*toInt64\(toUnixTimestamp\(min\(window_start\)\) \* 1000\) AS first_ms`).
		WithArgs("tenant-1", "tenant-1", "tenant-1", "tenant-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"first_ms"}).AddRow(first.UnixMilli()))

	got, found, err := bs.earliestCanonicalBillingFact(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("earliestCanonicalBillingFact error: %v", err)
	}
	if !found {
		t.Fatal("expected cursor seed to be found")
	}
	if !got.Equal(first) {
		t.Fatalf("cursor seed = %s, want %s", got, first)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestQueryTenantViewerMetricsCanonical(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	bs := &BillingSummarizer{clickhouse: db, logger: logging.NewLogger()}
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)

	// Canonical billing query is a two-step CTE + LEFT ANTI JOIN over
	// viewer_sessions_final keyed on billable_at_ms. Mock expects the
	// canonical query and returns one row per (cluster_id, origin_cluster_id).
	rows := sqlmock.NewRows([]string{"cluster_id", "origin_cluster_id", "ingress_gb", "egress_gb", "viewer_hours", "unique_viewers"}).
		AddRow("cluster-a", "", 1.25, 12.5, 3.0, int64(42))
	mock.ExpectQuery(`WITH window_candidates AS`).
		WithArgs("tenant-1", start.UnixMilli(), end.UnixMilli(), "tenant-1", start.UnixMilli()).
		WillReturnRows(rows)

	got, err := bs.queryTenantViewerMetrics(context.Background(), "tenant-1", start, end)
	if err != nil {
		t.Fatalf("queryTenantViewerMetrics error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].ClusterID != "cluster-a" || got[0].OriginClusterID != "" {
		t.Fatalf("unexpected cluster attribution row: %#v", got[0])
	}
	if got[0].IngressGB != 1.25 || got[0].EgressGB != 12.5 || got[0].ViewerHours != 3.0 || got[0].UniqueViewers != 42 {
		t.Fatalf("unexpected metric values: %#v", got[0])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestQueryClusterStreamRuntimeReadsFinalizedFactsByProjectionTime(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	bs := &BillingSummarizer{clickhouse: db, logger: logging.NewLogger()}
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(5 * time.Minute)
	rows := sqlmock.NewRows([]string{"cluster_id", "max_viewers", "total_streams", "stream_hours"}).
		AddRow("cluster-a", 7, 2, 0.25)
	mock.ExpectQuery(`FROM periscope\.stream_sessions_final`).
		WithArgs("tenant-1", start.UnixMilli(), end.UnixMilli(), "tenant-1", start.UnixMilli()).
		WillReturnRows(rows)

	got, err := bs.queryClusterStreamRuntime(context.Background(), "tenant-1", start, end)
	if err != nil {
		t.Fatalf("queryClusterStreamRuntime error: %v", err)
	}
	if got["cluster-a"].MaxViewers != 7 || got["cluster-a"].TotalStreams != 2 || got["cluster-a"].StreamHours != 0.25 {
		t.Fatalf("unexpected stream runtime metrics: %#v", got["cluster-a"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestQueryClusterStorageProviderUsageReadsLedgerByProjectionTime(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	bs := &BillingSummarizer{clickhouse: db, logger: logging.NewLogger()}
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(5 * time.Minute)

	rows := sqlmock.NewRows([]string{
		"cluster_id",
		"storage_provider_tenant_id",
		"storage_provider_cluster_id",
		"storage_backend",
		"storage_scope",
		"gb_seconds",
	}).AddRow("cluster-a", "provider-tenant", "provider-cluster", "s3", "cold", 900.0)
	mock.ExpectQuery(`FROM periscope\.storage_gb_seconds_5m`).
		WithArgs("tenant-1", end.UnixMilli(), start.UnixMilli(), end.UnixMilli()).
		WillReturnRows(rows)

	got, err := bs.queryClusterStorageProviderUsage(context.Background(), "tenant-1", start, end)
	if err != nil {
		t.Fatalf("queryClusterStorageProviderUsage error: %v", err)
	}
	if len(got["cluster-a"]) != 1 {
		t.Fatalf("expected one provider row, got %#v", got)
	}
	rec := got["cluster-a"][0]
	if rec.Meter.Dimensions["storage_scope"] != "cold" || rec.Meter.Meter != "storage_gb_seconds_cold" || rec.Meter.Quantity != 900 {
		t.Fatalf("unexpected storage provider row: %#v", rec)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestWindowCompletionReportsCoverEveryFiveMinuteSlice(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	bs := &BillingSummarizer{
		sourceID: "eu-1", sourceRegion: "eu", systemTenantID: "11111111-1111-4111-8111-111111111111",
	}
	reports := bs.windowCompletionReports(start, start.Add(15*time.Minute))
	if len(reports) != 3 {
		t.Fatalf("completion reports = %d, want 3", len(reports))
	}
	for i, report := range reports {
		wantEnd := start.Add(time.Duration(i+1) * 5 * time.Minute)
		if report.ReportKind != "window_complete" || !report.Complete || report.PeriodEnd != wantEnd {
			t.Fatalf("unexpected completion report %d: %#v", i, report)
		}
		if report.Sequence != uint64(wantEnd.Unix()) || len(report.ReportID) != 64 {
			t.Fatalf("missing stable sequence/id on report %d: %#v", i, report)
		}
	}
}

func TestUsageMessageKeyOrdersFinalReportsBeforeWindowMarker(t *testing.T) {
	end := time.Date(2026, 8, 19, 12, 5, 0, 0, time.UTC)
	bs := &BillingSummarizer{sourceID: "eu-1"}
	finalized := models.UsageSummary{ReportKind: "finalized", TenantID: "tenant-a", PeriodEnd: end}
	marker := models.UsageSummary{ReportKind: "window_complete", TenantID: "system", PeriodEnd: end}
	if bs.usageMessageKey(finalized) != bs.usageMessageKey(marker) {
		t.Fatal("finalized reports and their completion marker must share a Kafka partition key")
	}
	reservation := models.UsageSummary{ReportKind: "reservation", TenantID: "tenant-a", PeriodEnd: end}
	if bs.usageMessageKey(reservation) != "tenant-a" {
		t.Fatalf("reservation key = %q, want tenant ordering", bs.usageMessageKey(reservation))
	}
}

func TestEnsureSourceActivationRejectsPersistedRegionChange(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	activatedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`INSERT INTO periscope\.metering_sources`).
		WithArgs("periscope-default", "us-east", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"activated_at", "source_region"}).AddRow(activatedAt, "eu-west"))

	bs := &BillingSummarizer{
		postgresQueries: meteringdb.New(db),
		sourceID:        "periscope-default",
		sourceRegion:    "us-east",
	}
	if _, err := bs.ensureSourceActivation(context.Background()); err == nil {
		t.Fatal("expected persisted source region mismatch to fail")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
