package heartbeat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	db *sql.DB
}

func NewReportStore(db *sql.DB) *SQLReportStore {
	return &SQLReportStore{db: db}
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

	var createdAt time.Time
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO skipper.skipper_reports (
			id,
			tenant_id,
			trigger,
			summary,
			metrics_reviewed,
			root_cause,
			recommendations,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		RETURNING created_at
	`,
		record.ID,
		record.TenantID,
		record.Trigger,
		record.Summary,
		metricsJSON,
		record.RootCause,
		recommendationsJSON,
	).Scan(&createdAt)
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,
			tenant_id,
			trigger,
			summary,
			metrics_reviewed,
			root_cause,
			recommendations,
			created_at,
			read_at
		FROM skipper.skipper_reports
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()

	var reports []ReportRecord
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reports: %w", err)
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

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skipper.skipper_reports WHERE tenant_id = $1`, tenantID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count reports: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,
			tenant_id,
			trigger,
			summary,
			metrics_reviewed,
			root_cause,
			recommendations,
			created_at,
			read_at
		FROM skipper.skipper_reports
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list reports paginated: %w", err)
	}
	defer rows.Close()

	var reports []ReportRecord
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, 0, err
		}
		reports = append(reports, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate reports: %w", err)
	}
	return reports, total, nil
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

	row := s.db.QueryRowContext(ctx, `
		SELECT id,
			tenant_id,
			trigger,
			summary,
			metrics_reviewed,
			root_cause,
			recommendations,
			created_at,
			read_at
		FROM skipper.skipper_reports
		WHERE id = $1 AND tenant_id = $2
	`, reportID, tenantID)

	return scanReportRow(row)
}

func (s *SQLReportStore) MarkRead(ctx context.Context, tenantID string, reportIDs []string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return 0, errors.New("tenant id is required")
	}

	var result sql.Result
	var err error
	if len(reportIDs) == 0 {
		result, err = s.db.ExecContext(ctx,
			`UPDATE skipper.skipper_reports SET read_at = NOW() WHERE tenant_id = $1 AND read_at IS NULL`,
			tenantID,
		)
	} else {
		args := make([]any, 0, len(reportIDs)+1)
		args = append(args, tenantID)
		placeholders := ""
		for i, id := range reportIDs {
			if i > 0 {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", i+2)
			args = append(args, id)
		}
		result, err = s.db.ExecContext(ctx,
			`UPDATE skipper.skipper_reports SET read_at = NOW() WHERE tenant_id = $1 AND id IN (`+placeholders+`) AND read_at IS NULL`,
			args...,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("mark reports read: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *SQLReportStore) UnreadCount(ctx context.Context, tenantID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("report store unavailable")
	}
	if tenantID == "" {
		return 0, errors.New("tenant id is required")
	}

	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skipper.skipper_reports WHERE tenant_id = $1 AND read_at IS NULL`,
		tenantID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unread reports: %w", err)
	}
	return count, nil
}

// scanner is an interface matching both *sql.Rows and *sql.Row.
type scanner interface {
	Scan(dest ...any) error
}

func scanReportFields(s scanner) (ReportRecord, error) {
	var report ReportRecord
	var metricsJSON []byte
	var recsJSON []byte
	if err := s.Scan(
		&report.ID,
		&report.TenantID,
		&report.Trigger,
		&report.Summary,
		&metricsJSON,
		&report.RootCause,
		&recsJSON,
		&report.CreatedAt,
		&report.ReadAt,
	); err != nil {
		return ReportRecord{}, fmt.Errorf("scan report: %w", err)
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

func scanReport(rows *sql.Rows) (ReportRecord, error) {
	return scanReportFields(rows)
}

func scanReportRow(row *sql.Row) (ReportRecord, error) {
	return scanReportFields(row)
}
