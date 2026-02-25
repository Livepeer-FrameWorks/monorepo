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
	TenantID             string
	SourceRoot           string
	PageURL              string
	ContentHash          string
	ETag                 string
	LastModified         string
	RawSize              int64
	LastFetchedAt        time.Time
	SitemapPriority      float64
	SitemapChangeFreq    string
	ConsecutiveUnchanged int
	ConsecutiveFailures  int
	SourceType           string
}

type PageCacheStore struct {
	db *sql.DB
}

func NewPageCacheStore(db *sql.DB) *PageCacheStore {
	return &PageCacheStore{db: db}
}

const pageCacheColumns = `tenant_id, source_root, page_url, content_hash, etag, last_modified, raw_size, last_fetched_at, sitemap_priority, sitemap_changefreq, consecutive_unchanged, consecutive_failures, source_type`

func (s *PageCacheStore) scanRow(row interface{ Scan(...any) error }) (*PageCache, error) {
	var pc PageCache
	var contentHash, etag, lastModified, changeFreq, sourceType sql.NullString
	var rawSize sql.NullInt64
	var sitemapPriority sql.NullFloat64
	err := row.Scan(
		&pc.TenantID,
		&pc.SourceRoot,
		&pc.PageURL,
		&contentHash,
		&etag,
		&lastModified,
		&rawSize,
		&pc.LastFetchedAt,
		&sitemapPriority,
		&changeFreq,
		&pc.ConsecutiveUnchanged,
		&pc.ConsecutiveFailures,
		&sourceType,
	)
	if err != nil {
		return nil, err
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
	if sitemapPriority.Valid {
		pc.SitemapPriority = sitemapPriority.Float64
	} else {
		pc.SitemapPriority = 0.5
	}
	if changeFreq.Valid {
		pc.SitemapChangeFreq = changeFreq.String
	}
	if sourceType.Valid {
		pc.SourceType = sourceType.String
	} else {
		pc.SourceType = "sitemap"
	}
	return &pc, nil
}

func (s *PageCacheStore) Get(ctx context.Context, tenantID, pageURL string) (*PageCache, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if pageURL == "" {
		return nil, errors.New("page url is required")
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT `+pageCacheColumns+` FROM skipper.skipper_page_cache WHERE tenant_id = $1 AND page_url = $2`,
		tenantID, pageURL)
	pc, err := s.scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get page cache: %w", err)
	}
	return pc, nil
}

// Upsert inserts or updates crawl-result fields for a page.
// Scheduling metadata (sitemap_priority, consecutive_unchanged, etc.) is
// preserved on conflict — use BulkUpsert for initial metadata and
// UpdateCrawlOutcome for counter updates.
func (s *PageCacheStore) Upsert(ctx context.Context, cache PageCache) error {
	if cache.TenantID == "" {
		return errors.New("tenant id is required")
	}
	if cache.PageURL == "" {
		return errors.New("page url is required")
	}

	sourceType := cache.SourceType
	if sourceType == "" {
		sourceType = "sitemap"
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skipper.skipper_page_cache
			(tenant_id, source_root, page_url, content_hash, etag, last_modified, raw_size, last_fetched_at, source_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, page_url)
		DO UPDATE SET content_hash = EXCLUDED.content_hash,
		              etag = EXCLUDED.etag,
		              last_modified = EXCLUDED.last_modified,
		              raw_size = EXCLUDED.raw_size,
		              last_fetched_at = EXCLUDED.last_fetched_at,
		              source_root = EXCLUDED.source_root
	`, cache.TenantID, cache.SourceRoot, cache.PageURL,
		nullString(cache.ContentHash), nullString(cache.ETag), nullString(cache.LastModified),
		nullInt64(cache.RawSize), cache.LastFetchedAt, sourceType)
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

	const cols = 13
	var b strings.Builder
	b.WriteString(`INSERT INTO skipper.skipper_page_cache (` + pageCacheColumns + `) VALUES `)

	args := make([]any, 0, len(caches)*cols)
	for i, cache := range caches {
		if i > 0 {
			b.WriteString(", ")
		}
		offset := i * cols
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			offset+1, offset+2, offset+3, offset+4, offset+5, offset+6, offset+7,
			offset+8, offset+9, offset+10, offset+11, offset+12, offset+13)
		sourceType := cache.SourceType
		if sourceType == "" {
			sourceType = "sitemap"
		}
		args = append(args,
			cache.TenantID, cache.SourceRoot, cache.PageURL,
			nullString(cache.ContentHash), nullString(cache.ETag), nullString(cache.LastModified),
			nullInt64(cache.RawSize), cache.LastFetchedAt,
			cache.SitemapPriority, nullString(cache.SitemapChangeFreq),
			cache.ConsecutiveUnchanged, cache.ConsecutiveFailures, sourceType)
	}
	b.WriteString(` ON CONFLICT (tenant_id, page_url) DO UPDATE SET
		content_hash = EXCLUDED.content_hash,
		etag = EXCLUDED.etag,
		last_modified = EXCLUDED.last_modified,
		raw_size = EXCLUDED.raw_size,
		last_fetched_at = EXCLUDED.last_fetched_at,
		source_root = EXCLUDED.source_root,
		sitemap_priority = EXCLUDED.sitemap_priority,
		sitemap_changefreq = EXCLUDED.sitemap_changefreq,
		source_type = EXCLUDED.source_type`)

	_, err := s.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("bulk upsert page cache: %w", err)
	}
	return nil
}

// ListForTenant returns all cached pages for a tenant, ordered by last_fetched_at ASC (stalest first).
func (s *PageCacheStore) ListForTenant(ctx context.Context, tenantID string) ([]PageCache, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pageCacheColumns+` FROM skipper.skipper_page_cache WHERE tenant_id = $1 ORDER BY last_fetched_at ASC`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list page cache: %w", err)
	}
	defer rows.Close()

	var result []PageCache
	for rows.Next() {
		pc, err := s.scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan page cache row: %w", err)
		}
		result = append(result, *pc)
	}
	return result, rows.Err()
}

// UpdateCrawlOutcome updates the consecutive counters after a page is processed.
// When changed is true, consecutive_unchanged resets to 0; otherwise it increments.
// When failed is true, consecutive_failures increments; otherwise it resets to 0.
func (s *PageCacheStore) UpdateCrawlOutcome(ctx context.Context, tenantID, pageURL string, changed, failed bool) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if pageURL == "" {
		return errors.New("page url is required")
	}

	var unchangedExpr, failuresExpr string
	if changed {
		unchangedExpr = "0"
	} else {
		unchangedExpr = "consecutive_unchanged + 1"
	}
	if failed {
		failuresExpr = "consecutive_failures + 1"
	} else {
		failuresExpr = "0"
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE skipper.skipper_page_cache
		SET consecutive_unchanged = `+unchangedExpr+`,
		    consecutive_failures = `+failuresExpr+`
		WHERE tenant_id = $1 AND page_url = $2
	`, tenantID, pageURL)
	if err != nil {
		return fmt.Errorf("update crawl outcome: %w", err)
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
