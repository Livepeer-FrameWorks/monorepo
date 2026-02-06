package heartbeat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReportStoreSavePersistsReport(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	createdAt := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO skipper\\.skipper_reports").WithArgs(
		sqlmock.AnyArg(),
		"tenant-a",
		"threshold",
		"summary",
		sqlmock.AnyArg(),
		"root cause",
		sqlmock.AnyArg(),
	).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(createdAt))

	record, err := store.Save(context.Background(), ReportRecord{
		TenantID:        "tenant-a",
		Trigger:         "threshold",
		Summary:         "summary",
		MetricsReviewed: []string{"avg_buffer"},
		RootCause:       "root cause",
		Recommendations: []Recommendation{{Text: "fix it", Confidence: "high"}},
	})
	if err != nil {
		t.Fatalf("save report: %v", err)
	}
	if record.CreatedAt.IsZero() {
		t.Fatalf("expected created_at set")
	}
	if record.TenantID != "tenant-a" {
		t.Fatalf("unexpected tenant id: %s", record.TenantID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportStoreListByTenantScopesTenant(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)

	metricsJSON, err := json.Marshal([]string{"metric"})
	if err != nil {
		t.Fatalf("metrics marshal: %v", err)
	}
	recsJSON, err := json.Marshal([]Recommendation{{Text: "rec"}})
	if err != nil {
		t.Fatalf("recs marshal: %v", err)
	}

	rows := sqlmock.NewRows([]string{
		"id",
		"tenant_id",
		"trigger",
		"summary",
		"metrics_reviewed",
		"root_cause",
		"recommendations",
		"created_at",
	}).AddRow(
		"report-id",
		"tenant-a",
		"heartbeat",
		"summary",
		metricsJSON,
		"root",
		recsJSON,
		time.Now().UTC(),
	)

	mock.ExpectQuery("FROM skipper\\.skipper_reports").WithArgs("tenant-a", 2).WillReturnRows(rows)

	reports, err := store.ListByTenant(context.Background(), "tenant-a", 2)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].TenantID != "tenant-a" {
		t.Fatalf("unexpected tenant id: %s", reports[0].TenantID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
