package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
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
	db      *sql.DB
	queries *skipperdb.Queries
}

type SourceSummary struct {
	SourceURL   string
	PageCount   int
	LastCrawlAt *time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, queries: skipperdb.New(db)}
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

	rows, err := s.queries.SearchKnowledge(ctx, skipperdb.SearchKnowledgeParams{
		Embedding: pgvector.NewVector(embedding), TenantID: tenantID,
		MinSimilarity: minSimilarity, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search knowledge: %w", err)
	}
	chunks := make([]Chunk, 0, len(rows))
	for _, row := range rows {
		chunk, err := decodeChunk(row.ID, row.TenantID, row.SourceUrl, row.SourceTitle, row.SourceType, row.ChunkText, row.ChunkIndex, row.Metadata, row.Similarity)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
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

	rows, err := s.queries.HybridSearchKnowledge(ctx, skipperdb.HybridSearchKnowledgeParams{
		VectorWeight: vectorSearchWeight, Embedding: pgvector.NewVector(embedding), TextWeight: textSearchWeight,
		SearchQuery: query, TenantID: tenantID, MinSimilarity: defaultMinSimilarity, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("hybrid search knowledge: %w", err)
	}
	chunks := make([]Chunk, 0, len(rows))
	for _, row := range rows {
		chunk, err := decodeChunk(row.ID, row.TenantID, row.SourceUrl, row.SourceTitle, row.SourceType, row.ChunkText, row.ChunkIndex, row.Metadata, row.Similarity)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func decodeChunk(id, tenantID, sourceURL, sourceTitle string, sourceType sql.NullString, text string, index int, metadataBytes []byte, similarity float64) (Chunk, error) {
	chunk := Chunk{ID: id, TenantID: tenantID, SourceURL: sourceURL, SourceTitle: sourceTitle, SourceType: sourceType.String, Text: text, Index: index, Similarity: similarity}
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &chunk.Metadata); err != nil {
			return Chunk{}, fmt.Errorf("decode metadata: %w", err)
		}
	}
	return chunk, nil
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
	queries := skipperdb.New(tx)

	for sourceURL, tenantID := range bySource {
		if execErr := queries.DeleteKnowledgeSourceChunks(ctx, skipperdb.DeleteKnowledgeSourceChunksParams{
			TenantID: tenantID, SourceUrl: sourceURL,
		}); execErr != nil {
			return fmt.Errorf("delete existing chunks: %w", execErr)
		}
	}

	for _, chunk := range chunks {
		metadataBytes, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("encode metadata: %w", err)
		}
		sourceRoot := sourceRootFromMetadata(chunk.Metadata, chunk.SourceURL)
		sourceType := sourceTypeFromMetadata(chunk.Metadata)
		if err := queries.InsertKnowledgeChunk(ctx, skipperdb.InsertKnowledgeChunkParams{
			TenantID: chunk.TenantID, SourceUrl: chunk.SourceURL, SourceTitle: chunk.SourceTitle,
			SourceRoot: sourceRoot, SourceType: nullableSourceType(sourceType), ChunkText: chunk.Text,
			ChunkIndex: int32(chunk.Index), Embedding: pgvector.NewVector(chunk.Embedding), Metadata: string(metadataBytes),
		}); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func nullableSourceType(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
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

	if query == "" {
		rows, err := s.queries.SearchKnowledgeFiltered(ctx, skipperdb.SearchKnowledgeFilteredParams{
			Embedding: pgvector.NewVector(embedding), TenantID: tenantID, SourceType: sourceType,
			MinSimilarity: defaultMinSimilarity, RowLimit: int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search filtered knowledge: %w", err)
		}
		chunks := make([]Chunk, 0, len(rows))
		for _, row := range rows {
			chunk, err := decodeChunk(row.ID, row.TenantID, row.SourceUrl, row.SourceTitle, row.SourceType, row.ChunkText, row.ChunkIndex, row.Metadata, row.Similarity)
			if err != nil {
				return nil, err
			}
			chunks = append(chunks, chunk)
		}
		return chunks, nil
	}

	rows, err := s.queries.HybridSearchKnowledgeFiltered(ctx, skipperdb.HybridSearchKnowledgeFilteredParams{
		VectorWeight: vectorSearchWeight, Embedding: pgvector.NewVector(embedding), TextWeight: textSearchWeight,
		SearchQuery: query, TenantID: tenantID, SourceType: sourceType,
		MinSimilarity: defaultMinSimilarity, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search filtered knowledge: %w", err)
	}
	chunks := make([]Chunk, 0, len(rows))
	for _, row := range rows {
		chunk, err := decodeChunk(row.ID, row.TenantID, row.SourceUrl, row.SourceTitle, row.SourceType, row.ChunkText, row.ChunkIndex, row.Metadata, row.Similarity)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func (s *Store) DeleteBySource(ctx context.Context, tenantID, sourceURL string) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if sourceURL == "" {
		return errors.New("source url is required")
	}
	if err := s.queries.DeleteKnowledgeBySource(ctx, skipperdb.DeleteKnowledgeBySourceParams{TenantID: tenantID, SourceUrl: sourceURL}); err != nil {
		return fmt.Errorf("delete by source: %w", err)
	}
	return nil
}

func (s *Store) ListSources(ctx context.Context, tenantID string) ([]SourceSummary, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}

	rows, err := s.queries.ListKnowledgeSources(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	var sources []SourceSummary
	for _, row := range rows {
		source := SourceSummary{SourceURL: row.SourceUrl, PageCount: int(row.PageCount)}
		if timestamp, ok := row.LastCrawlAt.(time.Time); ok {
			source.LastCrawlAt = &timestamp
		}
		sources = append(sources, source)
	}
	return sources, nil
}
