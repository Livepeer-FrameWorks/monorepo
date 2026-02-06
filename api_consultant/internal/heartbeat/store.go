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
}

type ReportStore interface {
	Save(ctx context.Context, record ReportRecord) (ReportRecord, error)
	ListByTenant(ctx context.Context, tenantID string, limit int) ([]ReportRecord, error)
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
			created_at
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
		var report ReportRecord
		var metricsJSON []byte
		var recsJSON []byte
		if err := rows.Scan(
			&report.ID,
			&report.TenantID,
			&report.Trigger,
			&report.Summary,
			&metricsJSON,
			&report.RootCause,
			&recsJSON,
			&report.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		if len(metricsJSON) > 0 {
			if err := json.Unmarshal(metricsJSON, &report.MetricsReviewed); err != nil {
				return nil, fmt.Errorf("decode metrics reviewed: %w", err)
			}
		}
		if len(recsJSON) > 0 {
			if err := json.Unmarshal(recsJSON, &report.Recommendations); err != nil {
				return nil, fmt.Errorf("decode recommendations: %w", err)
			}
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reports: %w", err)
	}

	return reports, nil
}
