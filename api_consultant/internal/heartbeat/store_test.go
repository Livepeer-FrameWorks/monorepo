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
		"read_at",
	}).AddRow(
		"report-id",
		"tenant-a",
		"heartbeat",
		"summary",
		metricsJSON,
		"root",
		recsJSON,
		time.Now().UTC(),
		nil,
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

func reportMockRows() *sqlmock.Rows {
	metricsJSON, _ := json.Marshal([]string{"avg_buffer"})
	recsJSON, _ := json.Marshal([]Recommendation{{Text: "reduce bitrate", Confidence: "high"}})
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "trigger", "summary",
		"metrics_reviewed", "root_cause", "recommendations",
		"created_at", "read_at",
	}).AddRow(
		"r-1", "tenant-a", "heartbeat", "summary",
		metricsJSON, "root", recsJSON, time.Now().UTC(), nil,
	)
}

func TestReportStoreListByTenantPaginated(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	mock.ExpectQuery("SELECT COUNT").WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("FROM skipper\\.skipper_reports").WithArgs("tenant-a", 10, 0).
		WillReturnRows(reportMockRows())

	reports, total, err := store.ListByTenantPaginated(context.Background(), "tenant-a", 10, 0)
	if err != nil {
		t.Fatalf("list paginated: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total=3, got %d", total)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].ReadAt != nil {
		t.Fatalf("expected unread (nil ReadAt)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportStoreGetByID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	mock.ExpectQuery("FROM skipper\\.skipper_reports").WithArgs("r-1", "tenant-a").
		WillReturnRows(reportMockRows())

	report, err := store.GetByID(context.Background(), "tenant-a", "r-1")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if report.ID != "r-1" {
		t.Fatalf("expected id r-1, got %s", report.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportStoreMarkReadAll(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	mock.ExpectExec("UPDATE skipper\\.skipper_reports SET read_at").WithArgs("tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 5))

	n, err := store.MarkRead(context.Background(), "tenant-a", nil)
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 marked, got %d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportStoreMarkReadSpecific(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	mock.ExpectExec("UPDATE skipper\\.skipper_reports SET read_at").
		WithArgs("tenant-a", "r-1", "r-2").
		WillReturnResult(sqlmock.NewResult(0, 2))

	n, err := store.MarkRead(context.Background(), "tenant-a", []string{"r-1", "r-2"})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 marked, got %d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportStoreUnreadCount(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewReportStore(db)
	mock.ExpectQuery("SELECT COUNT").WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))

	count, err := store.UnreadCount(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("unread count: %v", err)
	}
	if count != 7 {
		t.Fatalf("expected 7, got %d", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
