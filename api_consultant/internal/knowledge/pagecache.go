package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PageCache struct {
	TenantID      string
	SourceRoot    string
	PageURL       string
	ContentHash   string
	ETag          string
	LastModified  string
	RawSize       int64
	LastFetchedAt time.Time
}

type PageCacheStore struct {
	db *sql.DB
}

func NewPageCacheStore(db *sql.DB) *PageCacheStore {
	return &PageCacheStore{db: db}
}

func (s *PageCacheStore) Get(ctx context.Context, tenantID, pageURL string) (*PageCache, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if pageURL == "" {
		return nil, errors.New("page url is required")
	}

	var pc PageCache
	var contentHash, etag, lastModified sql.NullString
	var rawSize sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, source_root, page_url, content_hash, etag, last_modified, raw_size, last_fetched_at
		FROM skipper.skipper_page_cache
		WHERE tenant_id = $1 AND page_url = $2
	`, tenantID, pageURL).Scan(
		&pc.TenantID,
		&pc.SourceRoot,
		&pc.PageURL,
		&contentHash,
		&etag,
		&lastModified,
		&rawSize,
		&pc.LastFetchedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get page cache: %w", err)
	}
	if contentHash.Valid {
		pc.ContentHash = contentHash.String
	}
	if etag.Valid {
		pc.ETag = etag.String
	}
	if lastModified.Valid {
		pc.LastModified = lastModified.String
	}
	if rawSize.Valid {
		pc.RawSize = rawSize.Int64
	}
	return &pc, nil
}

func (s *PageCacheStore) Upsert(ctx context.Context, cache PageCache) error {
	if cache.TenantID == "" {
		return errors.New("tenant id is required")
	}
	if cache.PageURL == "" {
		return errors.New("page url is required")
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skipper.skipper_page_cache (tenant_id, source_root, page_url, content_hash, etag, last_modified, raw_size, last_fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, page_url)
		DO UPDATE SET content_hash = EXCLUDED.content_hash,
		              etag = EXCLUDED.etag,
		              last_modified = EXCLUDED.last_modified,
		              raw_size = EXCLUDED.raw_size,
		              last_fetched_at = EXCLUDED.last_fetched_at,
		              source_root = EXCLUDED.source_root
	`, cache.TenantID, cache.SourceRoot, cache.PageURL,
		nullString(cache.ContentHash), nullString(cache.ETag), nullString(cache.LastModified),
		nullInt64(cache.RawSize), cache.LastFetchedAt)
	if err != nil {
		return fmt.Errorf("upsert page cache: %w", err)
	}
	return nil
}

func (s *PageCacheStore) LastFetchedForSource(ctx context.Context, tenantID, sourceRoot string) (*time.Time, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if sourceRoot == "" {
		return nil, errors.New("source root is required")
	}

	var lastFetched sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(last_fetched_at) FROM skipper.skipper_page_cache
		WHERE tenant_id = $1 AND source_root = $2
	`, tenantID, sourceRoot).Scan(&lastFetched)
	if err != nil {
		return nil, fmt.Errorf("last fetched for source: %w", err)
	}
	if !lastFetched.Valid {
		return nil, nil
	}
	t := lastFetched.Time
	return &t, nil
}

func (s *PageCacheStore) DeleteBySource(ctx context.Context, tenantID, sourceRoot string) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if sourceRoot == "" {
		return errors.New("source root is required")
	}

	_, err := s.db.ExecContext(ctx, `
		DELETE FROM skipper.skipper_page_cache
		WHERE tenant_id = $1 AND source_root = $2
	`, tenantID, sourceRoot)
	if err != nil {
		return fmt.Errorf("delete page cache by source: %w", err)
	}
	return nil
}

func (s *PageCacheStore) BulkUpsert(ctx context.Context, caches []PageCache) error {
	if len(caches) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString(`INSERT INTO skipper.skipper_page_cache (tenant_id, source_root, page_url, content_hash, etag, last_modified, raw_size, last_fetched_at) VALUES `)

	args := make([]any, 0, len(caches)*8)
	for i, cache := range caches {
		if i > 0 {
			b.WriteString(", ")
		}
		offset := i * 8
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			offset+1, offset+2, offset+3, offset+4, offset+5, offset+6, offset+7, offset+8)
		args = append(args,
			cache.TenantID, cache.SourceRoot, cache.PageURL,
			nullString(cache.ContentHash), nullString(cache.ETag), nullString(cache.LastModified),
			nullInt64(cache.RawSize), cache.LastFetchedAt)
	}
	b.WriteString(` ON CONFLICT (tenant_id, page_url) DO UPDATE SET
		content_hash = EXCLUDED.content_hash,
		etag = EXCLUDED.etag,
		last_modified = EXCLUDED.last_modified,
		raw_size = EXCLUDED.raw_size,
		last_fetched_at = EXCLUDED.last_fetched_at,
		source_root = EXCLUDED.source_root`)

	_, err := s.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("bulk upsert page cache: %w", err)
	}
	return nil
}

func (s *PageCacheStore) CleanupStale(ctx context.Context, tenantID string, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM skipper.skipper_page_cache WHERE tenant_id = $1 AND last_fetched_at < $2`,
		tenantID, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("cleanup stale page cache: %w", err)
	}
	return res.RowsAffected()
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt64(n int64) sql.NullInt64 {
	if n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}
