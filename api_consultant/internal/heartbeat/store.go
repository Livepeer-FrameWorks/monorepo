package heartbeat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
	"github.com/google/uuid"
)

type ReportRecord struct {
	ID              string
	TenantID        string
	Trigger         string
	Summary         string
	MetricsReviewed []string
	RootCause       string
	Recommendations []Recommendation
	CreatedAt       time.Time
	ReadAt          *time.Time
}

type ReportStore interface {
	Save(ctx context.Context, record ReportRecord) (ReportRecord, error)
	ListByTenant(ctx context.Context, tenantID string, limit int) ([]ReportRecord, error)
	ListByTenantPaginated(ctx context.Context, tenantID string, limit, offset int) ([]ReportRecord, int, error)
	GetByID(ctx context.Context, tenantID, reportID string) (ReportRecord, error)
	MarkRead(ctx context.Context, tenantID string, reportIDs []string) (int, error)
	UnreadCount(ctx context.Context, tenantID string) (int, error)
}

type SQLReportStore struct {
	db      *sql.DB
	queries *skipperdb.Queries
}

func NewReportStore(db *sql.DB) *SQLReportStore {
	var queries *skipperdb.Queries
	if db != nil {
		queries = skipperdb.New(db)
	}
	return &SQLReportStore{db: db, queries: queries}
}

func (s *SQLReportStore) Save(ctx context.Context, record ReportRecord) (ReportRecord, error) {
	if s == nil || s.db == nil {
		return ReportRecord{}, errors.New("report store unavailable")
	}
	if record.TenantID == "" {
		return ReportRecord{}, errors.New("tenant id is required")
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}

	metricsJSON, err := json.Marshal(record.MetricsReviewed)
	if err != nil {
		return ReportRecord{}, fmt.Errorf("encode metrics reviewed: %w", err)
	}
	recommendationsJSON, err := json.Marshal(record.Recommendations)
	if err != nil {
		return ReportRecord{}, fmt.Errorf("encode recommendations: %w", err)
	}

	createdAt, err := s.queries.SaveReport(ctx, skipperdb.SaveReportParams{
		ID: record.ID, TenantID: record.TenantID, Trigger: record.Trigger, Summary: record.Summary,
		MetricsReviewed: string(metricsJSON), RootCause: record.RootCause, Recommendations: string(recommendationsJSON),
	})
	if err != nil {
		return ReportRecord{}, fmt.Errorf("insert report: %w", err)
	}

	record.CreatedAt = createdAt
	return record, nil
}

func (s *SQLReportStore) ListByTenant(ctx context.Context, tenantID string, limit int) ([]ReportRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.queries.ListReportsByTenant(ctx, skipperdb.ListReportsByTenantParams{
		TenantID: tenantID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	var reports []ReportRecord
	for _, row := range rows {
		r, err := decodeReport(row.ID, row.TenantID, row.Trigger, row.Summary, row.MetricsReviewed, row.RootCause, row.Recommendations, row.CreatedAt, row.ReadAt)
		if err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, nil
}

func (s *SQLReportStore) ListByTenantPaginated(ctx context.Context, tenantID string, limit, offset int) ([]ReportRecord, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return nil, 0, errors.New("tenant id is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountReportsByTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, fmt.Errorf("count reports: %w", err)
	}

	rows, err := s.queries.ListReportsByTenantPaginated(ctx, skipperdb.ListReportsByTenantPaginatedParams{
		TenantID: tenantID, RowLimit: int32(limit), RowOffset: int32(offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list reports paginated: %w", err)
	}
	var reports []ReportRecord
	for _, row := range rows {
		r, err := decodeReport(row.ID, row.TenantID, row.Trigger, row.Summary, row.MetricsReviewed, row.RootCause, row.Recommendations, row.CreatedAt, row.ReadAt)
		if err != nil {
			return nil, 0, err
		}
		reports = append(reports, r)
	}
	return reports, int(total), nil
}

func (s *SQLReportStore) GetByID(ctx context.Context, tenantID, reportID string) (ReportRecord, error) {
	if s == nil || s.db == nil {
		return ReportRecord{}, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return ReportRecord{}, errors.New("tenant id is required")
	}
	if reportID == "" {
		return ReportRecord{}, errors.New("report id is required")
	}

	row, err := s.queries.GetReportByID(ctx, skipperdb.GetReportByIDParams{ID: reportID, TenantID: tenantID})
	if err != nil {
		return ReportRecord{}, fmt.Errorf("scan report: %w", err)
	}
	return decodeReport(row.ID, row.TenantID, row.Trigger, row.Summary, row.MetricsReviewed, row.RootCause, row.Recommendations, row.CreatedAt, row.ReadAt)
}

func (s *SQLReportStore) MarkRead(ctx context.Context, tenantID string, reportIDs []string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return 0, errors.New("tenant id is required")
	}

	var affected int64
	var err error
	if len(reportIDs) == 0 {
		affected, err = s.queries.MarkAllReportsRead(ctx, tenantID)
	} else {
		affected, err = s.queries.MarkReportsRead(ctx, skipperdb.MarkReportsReadParams{TenantID: tenantID, Ids: reportIDs})
	}
	if err != nil {
		return 0, fmt.Errorf("mark reports read: %w", err)
	}
	return int(affected), nil
}

func (s *SQLReportStore) UnreadCount(ctx context.Context, tenantID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return 0, errors.New("tenant id is required")
	}

	count, err := s.queries.CountUnreadReports(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("count unread reports: %w", err)
	}
	return int(count), nil
}

func decodeReport(id, tenantID, trigger, summary string, metricsJSON []byte, rootCause string, recsJSON []byte, createdAt time.Time, readAt sql.NullTime) (ReportRecord, error) {
	report := ReportRecord{
		ID: id, TenantID: tenantID, Trigger: trigger, Summary: summary,
		RootCause: rootCause, CreatedAt: createdAt,
	}
	if readAt.Valid {
		report.ReadAt = &readAt.Time
	}
	if len(metricsJSON) > 0 {
		if err := json.Unmarshal(metricsJSON, &report.MetricsReviewed); err != nil {
			return ReportRecord{}, fmt.Errorf("decode metrics reviewed: %w", err)
		}
	}
	if len(recsJSON) > 0 {
		if err := json.Unmarshal(recsJSON, &report.Recommendations); err != nil {
			return ReportRecord{}, fmt.Errorf("decode recommendations: %w", err)
		}
	}
	return report, nil
}
