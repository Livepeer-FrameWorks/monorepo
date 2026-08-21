package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_consultant/internal/database/skipperdb"
)

type PostStore interface {
	Save(ctx context.Context, record PostRecord) (PostRecord, error)
	CountToday(ctx context.Context) (int, error)
	ListRecent(ctx context.Context, limit int) ([]PostRecord, error)
	MarkSent(ctx context.Context, id string) error
}

type SQLPostStore struct {
	db       *sql.DB
	queries  *skipperdb.Queries
	tenantID string
}

func NewPostStore(db *sql.DB, tenantID string) *SQLPostStore {
	var queries *skipperdb.Queries
	if db != nil {
		queries = skipperdb.New(db)
	}
	return &SQLPostStore{db: db, queries: queries, tenantID: tenantID}
}

func (s *SQLPostStore) Save(ctx context.Context, record PostRecord) (PostRecord, error) {
	if s == nil || s.db == nil {
		return PostRecord{}, errors.New("post store unavailable")
	}

	triggerJSON, err := json.Marshal(record.TriggerData)
	if err != nil {
		return PostRecord{}, fmt.Errorf("encode trigger data: %w", err)
	}

	status := record.Status
	if status == "" {
		status = "draft"
	}

	row, err := s.queries.SaveSocialPost(ctx, skipperdb.SaveSocialPostParams{
		TenantID: s.tenantID, ContentType: string(record.ContentType), TweetText: record.TweetText,
		ContextSummary: sql.NullString{String: record.ContextSummary, Valid: record.ContextSummary != ""},
		TriggerData:    string(triggerJSON), Status: status,
	})
	if err != nil {
		return PostRecord{}, fmt.Errorf("insert post: %w", err)
	}

	record.ID = row.ID
	record.Status = status
	record.CreatedAt = row.CreatedAt
	return record, nil
}

func (s *SQLPostStore) CountToday(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("post store unavailable")
	}

	count, err := s.queries.CountSocialPostsToday(ctx, s.tenantID)
	if err != nil {
		return 0, fmt.Errorf("count today posts: %w", err)
	}
	return int(count), nil
}

func (s *SQLPostStore) ListRecent(ctx context.Context, limit int) ([]PostRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("post store unavailable")
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.queries.ListRecentSocialPosts(ctx, skipperdb.ListRecentSocialPostsParams{
		TenantID: s.tenantID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list recent posts: %w", err)
	}
	var posts []PostRecord
	for _, row := range rows {
		p, err := decodePost(row)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, nil
}

func (s *SQLPostStore) MarkSent(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return errors.New("post store unavailable")
	}

	err := s.queries.MarkSocialPostSent(ctx, skipperdb.MarkSocialPostSentParams{ID: id, TenantID: s.tenantID})
	if err != nil {
		return fmt.Errorf("mark post sent: %w", err)
	}
	return nil
}

func decodePost(row skipperdb.ListRecentSocialPostsRow) (PostRecord, error) {
	post := PostRecord{
		ID: row.ID, ContentType: ContentType(row.ContentType), TweetText: row.TweetText,
		ContextSummary: row.ContextSummary.String, Status: row.Status, CreatedAt: row.CreatedAt,
	}
	if row.SentAt.Valid {
		post.SentAt = &row.SentAt.Time
	}
	if len(row.TriggerData) > 0 {
		if err := json.Unmarshal(row.TriggerData, &post.TriggerData); err != nil {
			return PostRecord{}, fmt.Errorf("decode trigger data: %w", err)
		}
	}
	return post, nil
}
