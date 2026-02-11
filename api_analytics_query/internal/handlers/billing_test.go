package handlers

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"frameworks/pkg/logging"

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

func TestAttributedViewerClusterID(t *testing.T) {
	tests := []struct {
		name             string
		clusterID        string
		originClusterID  string
		primaryClusterID string
		expected         string
	}{
		{name: "uses serving cluster when present", clusterID: "cluster-serving", originClusterID: "cluster-origin", primaryClusterID: "cluster-primary", expected: "cluster-serving"},
		{name: "falls back to origin cluster when serving missing", clusterID: "", originClusterID: "cluster-origin", primaryClusterID: "cluster-primary", expected: "cluster-origin"},
		{name: "falls back to primary when both missing", clusterID: "", originClusterID: "", primaryClusterID: "cluster-primary", expected: "cluster-primary"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := attributedViewerClusterID(test.clusterID, test.originClusterID, test.primaryClusterID)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestMissingColumnCompatibilityError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unknown expression", err: fmt.Errorf("Code: 47, Unknown expression identifier origin_cluster_id"), want: true},
		{name: "missing columns", err: fmt.Errorf("Code: 47, Missing columns: 'cluster_id'"), want: true},
		{name: "other error", err: fmt.Errorf("connection refused"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingColumnCompatibilityError(tt.err); got != tt.want {
				t.Fatalf("isMissingColumnCompatibilityError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryTenantViewerMetricsFallsBackForLegacySchema(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	bs := &BillingSummarizer{clickhouse: db, logger: logging.NewLogger()}
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)

	mock.ExpectQuery(`SELECT\s+cluster_id,\s+origin_cluster_id`).
		WithArgs("tenant-1", start, end).
		WillReturnError(fmt.Errorf("Code: 47, Unknown expression identifier `origin_cluster_id`"))

	rows := sqlmock.NewRows([]string{"cluster_id", "egress_gb", "viewer_hours", "unique_viewers"}).
		AddRow("cluster-a", 12.5, 3.0, 42)
	mock.ExpectQuery(`SELECT\s+cluster_id,\s+COALESCE\(sum\(egress_gb\), 0\) as egress_gb`).
		WithArgs("tenant-1", start, end).
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
	if got[0].EgressGB != 12.5 || got[0].ViewerHours != 3.0 || got[0].UniqueViewers != 42 {
		t.Fatalf("unexpected metric values: %#v", got[0])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
