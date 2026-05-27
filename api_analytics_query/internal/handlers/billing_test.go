package handlers

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

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
	mock.ExpectQuery(`SELECT min\(first_ms\).*toUnixTimestamp\(min\(window_start\)\) \* 1000 AS first_ms`).
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
	if rec.StorageScope != "cold" || rec.UsageType != "storage_gb_seconds_cold" || rec.GBSeconds != 900 {
		t.Fatalf("unexpected storage provider row: %#v", rec)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
