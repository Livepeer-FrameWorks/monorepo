package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
)

type Chunk struct {
	ID          string
	TenantID    string
	SourceURL   string
	SourceTitle string
	Text        string
	Index       int
	Embedding   []float32
	Metadata    map[string]any
	Similarity  float64
}

type Store struct {
	db *sql.DB
}

type SourceSummary struct {
	SourceURL   string
	PageCount   int
	LastCrawlAt *time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]Chunk, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if len(embedding) == 0 {
		return nil, errors.New("embedding is required")
	}
	if limit <= 0 {
		limit = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,
			tenant_id,
			source_url,
			source_title,
			chunk_text,
			chunk_index,
			metadata,
			1 - (embedding <=> $2) AS similarity
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		ORDER BY embedding <=> $2
		LIMIT $3
	`, tenantID, pgvector.NewVector(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("search knowledge: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var metadataBytes []byte
		if err := rows.Scan(
			&chunk.ID,
			&chunk.TenantID,
			&chunk.SourceURL,
			&chunk.SourceTitle,
			&chunk.Text,
			&chunk.Index,
			&metadataBytes,
			&chunk.Similarity,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge chunk: %w", err)
		}
		if len(metadataBytes) > 0 {
			if err := json.Unmarshal(metadataBytes, &chunk.Metadata); err != nil {
				return nil, fmt.Errorf("decode metadata: %w", err)
			}
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge chunks: %w", err)
	}

	return chunks, nil
}

func (s *Store) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	bySource := make(map[string]string)
	for _, chunk := range chunks {
		if chunk.TenantID == "" {
			return errors.New("tenant id is required for chunk")
		}
		if chunk.SourceURL == "" {
			return errors.New("source url is required for chunk")
		}
		bySource[chunk.SourceURL] = chunk.TenantID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for sourceURL, tenantID := range bySource {
		if _, execErr := tx.ExecContext(ctx, `
			DELETE FROM skipper.skipper_knowledge
			WHERE tenant_id = $1 AND source_url = $2
		`, tenantID, sourceURL); execErr != nil {
			return fmt.Errorf("delete existing chunks: %w", execErr)
		}
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO skipper.skipper_knowledge (
			tenant_id,
			source_url,
			source_title,
			chunk_text,
			chunk_index,
			embedding,
			metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, chunk := range chunks {
		metadataBytes, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("encode metadata: %w", err)
		}
		if _, err := stmt.ExecContext(
			ctx,
			chunk.TenantID,
			chunk.SourceURL,
			chunk.SourceTitle,
			chunk.Text,
			chunk.Index,
			pgvector.NewVector(chunk.Embedding),
			metadataBytes,
		); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func (s *Store) DeleteBySource(ctx context.Context, tenantID, sourceURL string) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if sourceURL == "" {
		return errors.New("source url is required")
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		  AND (source_url = $2 OR metadata->>'source_root' = $2)
	`, tenantID, sourceURL); err != nil {
		return fmt.Errorf("delete by source: %w", err)
	}
	return nil
}

func (s *Store) ListSources(ctx context.Context, tenantID string) ([]SourceSummary, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(NULLIF(metadata->>'source_root', ''), source_url) AS source_url,
			COUNT(DISTINCT COALESCE(NULLIF(metadata->>'page_url', ''), source_url)) AS page_count,
			MAX(NULLIF(metadata->>'ingested_at', '')::timestamptz) AS last_crawl_at
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		GROUP BY COALESCE(NULLIF(metadata->>'source_root', ''), source_url)
		ORDER BY source_url
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()

	var sources []SourceSummary
	for rows.Next() {
		var source SourceSummary
		var lastCrawl sql.NullTime
		if err := rows.Scan(&source.SourceURL, &source.PageCount, &lastCrawl); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		if lastCrawl.Valid {
			t := lastCrawl.Time
			source.LastCrawlAt = &t
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sources: %w", err)
	}

	return sources, nil
}
