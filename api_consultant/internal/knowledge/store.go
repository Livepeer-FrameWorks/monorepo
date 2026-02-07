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
	SourceType  string
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

const defaultMinSimilarity = 0.3

func (s *Store) Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]Chunk, error) {
	return s.SearchWithThreshold(ctx, tenantID, embedding, limit, defaultMinSimilarity)
}

func (s *Store) SearchWithThreshold(ctx context.Context, tenantID string, embedding []float32, limit int, minSimilarity float64) ([]Chunk, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if len(embedding) == 0 {
		return nil, errors.New("embedding is required")
	}
	if limit <= 0 {
		limit = 5
	}
	if minSimilarity < 0 {
		minSimilarity = 0
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,
			tenant_id,
			source_url,
			source_title,
			source_type,
			chunk_text,
			chunk_index,
			metadata,
			1 - (embedding <=> $2) AS similarity
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		  AND 1 - (embedding <=> $2) > $4
		ORDER BY embedding <=> $2
		LIMIT $3
	`, tenantID, pgvector.NewVector(embedding), limit, minSimilarity)
	if err != nil {
		return nil, fmt.Errorf("search knowledge: %w", err)
	}
	defer rows.Close()

	return scanChunks(rows)
}

const (
	vectorSearchWeight = 0.7
	textSearchWeight   = 0.3
)

// HybridSearch combines vector similarity with full-text relevance scoring.
// The final score is 0.7 * cosine_similarity + 0.3 * ts_rank.
// Falls back to vector-only search when query is empty.
func (s *Store) HybridSearch(ctx context.Context, tenantID string, embedding []float32, query string, limit int) ([]Chunk, error) {
	if query == "" {
		return s.Search(ctx, tenantID, embedding, limit)
	}
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
			source_type,
			chunk_text,
			chunk_index,
			metadata,
			$5 * (1 - (embedding <=> $2))
				+ $6 * COALESCE(ts_rank(tsv, plainto_tsquery('english', $4)), 0) AS similarity
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		  AND 1 - (embedding <=> $2) > $7
		ORDER BY similarity DESC
		LIMIT $3
	`, tenantID, pgvector.NewVector(embedding), limit, query,
		vectorSearchWeight, textSearchWeight, defaultMinSimilarity)
	if err != nil {
		return nil, fmt.Errorf("hybrid search knowledge: %w", err)
	}
	defer rows.Close()

	return scanChunks(rows)
}

func scanChunks(rows *sql.Rows) ([]Chunk, error) {
	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var metadataBytes []byte
		var sourceType sql.NullString
		if err := rows.Scan(
			&chunk.ID,
			&chunk.TenantID,
			&chunk.SourceURL,
			&chunk.SourceTitle,
			&sourceType,
			&chunk.Text,
			&chunk.Index,
			&metadataBytes,
			&chunk.Similarity,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge chunk: %w", err)
		}
		if sourceType.Valid {
			chunk.SourceType = sourceType.String
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

	// Under PostgreSQL READ COMMITTED (the default), concurrent readers
	// continue to see the old rows until this transaction commits.
	// The delete-then-insert is atomic from the perspective of other sessions.
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
			source_root,
			source_type,
			chunk_text,
			chunk_index,
			embedding,
			metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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
		sourceRoot := sourceRootFromMetadata(chunk.Metadata, chunk.SourceURL)
		sourceType := sourceTypeFromMetadata(chunk.Metadata)
		if _, err := stmt.ExecContext(
			ctx,
			chunk.TenantID,
			chunk.SourceURL,
			chunk.SourceTitle,
			sourceRoot,
			sourceType,
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

func sourceRootFromMetadata(metadata map[string]any, fallback string) string {
	if metadata != nil {
		if sr, ok := metadata["source_root"].(string); ok && sr != "" {
			return sr
		}
	}
	return fallback
}

func sourceTypeFromMetadata(metadata map[string]any) *string {
	if metadata != nil {
		if st, ok := metadata["source_type"].(string); ok && st != "" {
			return &st
		}
	}
	return nil
}

// SearchFiltered is like HybridSearch but also filters by source_type when non-empty.
func (s *Store) SearchFiltered(ctx context.Context, tenantID string, embedding []float32, query string, limit int, sourceType string) ([]Chunk, error) {
	if sourceType == "" {
		return s.HybridSearch(ctx, tenantID, embedding, query, limit)
	}
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if len(embedding) == 0 {
		return nil, errors.New("embedding is required")
	}
	if limit <= 0 {
		limit = 5
	}

	q := `
		SELECT id,
			tenant_id,
			source_url,
			source_title,
			source_type,
			chunk_text,
			chunk_index,
			metadata,
			$5 * (1 - (embedding <=> $2))
				+ $6 * COALESCE(ts_rank(tsv, plainto_tsquery('english', $4)), 0) AS similarity
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		  AND source_type = $8
		  AND 1 - (embedding <=> $2) > $7
		ORDER BY similarity DESC
		LIMIT $3
	`
	if query == "" {
		q = `
		SELECT id,
			tenant_id,
			source_url,
			source_title,
			source_type,
			chunk_text,
			chunk_index,
			metadata,
			1 - (embedding <=> $2) AS similarity
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		  AND source_type = $4
		  AND 1 - (embedding <=> $2) > $5
		ORDER BY embedding <=> $2
		LIMIT $3
		`
		rows, err := s.db.QueryContext(ctx, q, tenantID, pgvector.NewVector(embedding), limit, sourceType, defaultMinSimilarity)
		if err != nil {
			return nil, fmt.Errorf("search filtered knowledge: %w", err)
		}
		defer rows.Close()
		return scanChunks(rows)
	}

	rows, err := s.db.QueryContext(ctx, q, tenantID, pgvector.NewVector(embedding), limit, query,
		vectorSearchWeight, textSearchWeight, defaultMinSimilarity, sourceType)
	if err != nil {
		return nil, fmt.Errorf("search filtered knowledge: %w", err)
	}
	defer rows.Close()
	return scanChunks(rows)
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
		  AND (source_url = $2 OR source_root = $2)
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
			COALESCE(source_root, source_url) AS source_url,
			COUNT(DISTINCT source_url) AS page_count,
			MAX(NULLIF(metadata->>'ingested_at', '')::timestamptz) AS last_crawl_at
		FROM skipper.skipper_knowledge
		WHERE tenant_id = $1
		GROUP BY COALESCE(source_root, source_url)
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
