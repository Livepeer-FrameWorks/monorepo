package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
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
	db      *sql.DB
	queries *skipperdb.Queries
}

func NewPageCacheStore(db *sql.DB) *PageCacheStore {
	return &PageCacheStore{db: db, queries: skipperdb.New(db)}
}

func (s *PageCacheStore) Get(ctx context.Context, tenantID, pageURL string) (*PageCache, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	if pageURL == "" {
		return nil, errors.New("page url is required")
	}

	row, err := s.queries.GetPageCache(ctx, skipperdb.GetPageCacheParams{TenantID: tenantID, PageUrl: pageURL})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get page cache: %w", err)
	}
	pc := pageCacheFromValues(row.TenantID, row.SourceRoot, row.PageUrl, row.ContentHash, row.Etag, row.LastModified, row.RawSize, row.LastFetchedAt, row.SitemapPriority, row.SitemapChangefreq, row.ConsecutiveUnchanged, row.ConsecutiveFailures, row.SourceType)
	return &pc, nil
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

	err := s.queries.UpsertPageCache(ctx, skipperdb.UpsertPageCacheParams{
		TenantID: cache.TenantID, SourceRoot: cache.SourceRoot, PageUrl: cache.PageURL,
		ContentHash: nullString(cache.ContentHash), Etag: nullString(cache.ETag), LastModified: nullString(cache.LastModified),
		RawSize: nullInt64(cache.RawSize), LastFetchedAt: cache.LastFetchedAt, SourceType: sourceType,
	})
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

	lastFetched, err := s.queries.LastFetchedForSource(ctx, skipperdb.LastFetchedForSourceParams{TenantID: tenantID, SourceRoot: sourceRoot})
	if err != nil {
		return nil, fmt.Errorf("last fetched for source: %w", err)
	}
	timestamp, ok := lastFetched.(time.Time)
	if !ok {
		return nil, nil
	}
	return &timestamp, nil
}

func (s *PageCacheStore) DeleteBySource(ctx context.Context, tenantID, sourceRoot string) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if sourceRoot == "" {
		return errors.New("source root is required")
	}

	err := s.queries.DeletePageCacheBySource(ctx, skipperdb.DeletePageCacheBySourceParams{TenantID: tenantID, SourceRoot: sourceRoot})
	if err != nil {
		return fmt.Errorf("delete page cache by source: %w", err)
	}
	return nil
}

func (s *PageCacheStore) BulkUpsert(ctx context.Context, caches []PageCache) error {
	if len(caches) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin bulk upsert page cache: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	queries := skipperdb.New(tx)
	for _, cache := range caches {
		sourceType := cache.SourceType
		if sourceType == "" {
			sourceType = "sitemap"
		}
		if err := queries.UpsertPageCacheWithScheduling(ctx, skipperdb.UpsertPageCacheWithSchedulingParams{
			TenantID: cache.TenantID, SourceRoot: cache.SourceRoot, PageUrl: cache.PageURL,
			ContentHash: nullString(cache.ContentHash), Etag: nullString(cache.ETag), LastModified: nullString(cache.LastModified),
			RawSize: nullInt64(cache.RawSize), LastFetchedAt: cache.LastFetchedAt,
			SitemapPriority: sql.NullFloat64{Float64: cache.SitemapPriority, Valid: true}, SitemapChangefreq: nullString(cache.SitemapChangeFreq),
			ConsecutiveUnchanged: int32(cache.ConsecutiveUnchanged), ConsecutiveFailures: int32(cache.ConsecutiveFailures), SourceType: sourceType,
		}); err != nil {
			return fmt.Errorf("bulk upsert page cache: %w", err)
		}
	}
	return tx.Commit()
}

// ListForTenant returns all cached pages for a tenant, ordered by last_fetched_at ASC (stalest first).
func (s *PageCacheStore) ListForTenant(ctx context.Context, tenantID string) ([]PageCache, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}

	rows, err := s.queries.ListPageCacheForTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list page cache: %w", err)
	}
	var result []PageCache
	for _, row := range rows {
		result = append(result, pageCacheFromValues(row.TenantID, row.SourceRoot, row.PageUrl, row.ContentHash, row.Etag, row.LastModified, row.RawSize, row.LastFetchedAt, row.SitemapPriority, row.SitemapChangefreq, row.ConsecutiveUnchanged, row.ConsecutiveFailures, row.SourceType))
	}
	return result, nil
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

	err := s.queries.UpdatePageCrawlOutcome(ctx, skipperdb.UpdatePageCrawlOutcomeParams{
		Changed: changed, Failed: failed, TenantID: tenantID, PageUrl: pageURL,
	})
	if err != nil {
		return fmt.Errorf("update crawl outcome: %w", err)
	}
	return nil
}

func (s *PageCacheStore) CleanupStale(ctx context.Context, tenantID string, olderThan time.Duration) (int64, error) {
	affected, err := s.queries.CleanupStalePageCache(ctx, skipperdb.CleanupStalePageCacheParams{
		TenantID: tenantID, Cutoff: time.Now().Add(-olderThan),
	})
	if err != nil {
		return 0, fmt.Errorf("cleanup stale page cache: %w", err)
	}
	return affected, nil
}

func pageCacheFromValues(tenantID, sourceRoot, pageURL string, contentHash, etag, lastModified sql.NullString, rawSize sql.NullInt64, lastFetchedAt time.Time, sitemapPriority sql.NullFloat64, changeFreq sql.NullString, unchanged, failures int32, sourceType string) PageCache {
	priority := 0.5
	if sitemapPriority.Valid {
		priority = sitemapPriority.Float64
	}
	if sourceType == "" {
		sourceType = "sitemap"
	}
	return PageCache{
		TenantID: tenantID, SourceRoot: sourceRoot, PageURL: pageURL,
		ContentHash: contentHash.String, ETag: etag.String, LastModified: lastModified.String,
		RawSize: rawSize.Int64, LastFetchedAt: lastFetchedAt, SitemapPriority: priority,
		SitemapChangeFreq: changeFreq.String, ConsecutiveUnchanged: int(unchanged),
		ConsecutiveFailures: int(failures), SourceType: sourceType,
	}
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
