package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PostStore interface {
	Save(ctx context.Context, record PostRecord) (PostRecord, error)
	CountToday(ctx context.Context) (int, error)
	ListRecent(ctx context.Context, limit int) ([]PostRecord, error)
	MarkSent(ctx context.Context, id string) error
}

type SQLPostStore struct {
	db *sql.DB
}

func NewPostStore(db *sql.DB) *SQLPostStore {
	return &SQLPostStore{db: db}
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

	var createdAt time.Time
	var id string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO skipper.skipper_posts (
			content_type,
			tweet_text,
			context_summary,
			trigger_data,
			status,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, NOW())
		RETURNING id, created_at
	`,
		string(record.ContentType),
		record.TweetText,
		record.ContextSummary,
		triggerJSON,
		status,
	).Scan(&id, &createdAt)
	if err != nil {
		return PostRecord{}, fmt.Errorf("insert post: %w", err)
	}

	record.ID = id
	record.Status = status
	record.CreatedAt = createdAt
	return record, nil
}

func (s *SQLPostStore) CountToday(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("post store unavailable")
	}

	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM skipper.skipper_posts
		WHERE status IN ('draft', 'sent', 'posted')
		AND created_at >= (CURRENT_DATE AT TIME ZONE 'UTC')
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count today posts: %w", err)
	}
	return count, nil
}

func (s *SQLPostStore) ListRecent(ctx context.Context, limit int) ([]PostRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("post store unavailable")
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,
			content_type,
			tweet_text,
			context_summary,
			trigger_data,
			status,
			sent_at,
			created_at
		FROM skipper.skipper_posts
		WHERE status IN ('draft', 'sent', 'posted', 'baseline')
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent posts: %w", err)
	}
	defer rows.Close()

	var posts []PostRecord
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate posts: %w", err)
	}
	return posts, nil
}

func (s *SQLPostStore) MarkSent(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return errors.New("post store unavailable")
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE skipper.skipper_posts SET status = 'sent', sent_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark post sent: %w", err)
	}
	return nil
}

type postScanner interface {
	Scan(dest ...any) error
}

func scanPost(s postScanner) (PostRecord, error) {
	var post PostRecord
	var contentType string
	var contextSummary sql.NullString
	var triggerJSON []byte
	if err := s.Scan(
		&post.ID,
		&contentType,
		&post.TweetText,
		&contextSummary,
		&triggerJSON,
		&post.Status,
		&post.SentAt,
		&post.CreatedAt,
	); err != nil {
		return PostRecord{}, fmt.Errorf("scan post: %w", err)
	}
	post.ContentType = ContentType(contentType)
	if contextSummary.Valid {
		post.ContextSummary = contextSummary.String
	}
	if len(triggerJSON) > 0 {
		if err := json.Unmarshal(triggerJSON, &post.TriggerData); err != nil {
			return PostRecord{}, fmt.Errorf("decode trigger data: %w", err)
		}
	}
	return post, nil
}
