-- name: GetPageCache :one
SELECT tenant_id, source_root, page_url, content_hash, etag, last_modified,
       raw_size, last_fetched_at, sitemap_priority, sitemap_changefreq,
       consecutive_unchanged, consecutive_failures, source_type
FROM skipper.skipper_page_cache
WHERE tenant_id = sqlc.arg(tenant_id)
  AND page_url = sqlc.arg(page_url);

-- name: UpsertPageCache :exec
INSERT INTO skipper.skipper_page_cache (
    tenant_id, source_root, page_url, content_hash, etag, last_modified,
    raw_size, last_fetched_at, source_type
)
VALUES (
    sqlc.arg(tenant_id), sqlc.arg(source_root), sqlc.arg(page_url),
    sqlc.narg(content_hash), sqlc.narg(etag), sqlc.narg(last_modified),
    sqlc.narg(raw_size), sqlc.arg(last_fetched_at), sqlc.arg(source_type)
)
ON CONFLICT (tenant_id, page_url) DO UPDATE
SET content_hash = EXCLUDED.content_hash,
    etag = EXCLUDED.etag,
    last_modified = EXCLUDED.last_modified,
    raw_size = EXCLUDED.raw_size,
    last_fetched_at = EXCLUDED.last_fetched_at,
    source_root = EXCLUDED.source_root;

-- name: LastFetchedForSource :one
SELECT MAX(last_fetched_at)
FROM skipper.skipper_page_cache
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_root = sqlc.arg(source_root);

-- name: DeletePageCacheBySource :exec
DELETE FROM skipper.skipper_page_cache
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_root = sqlc.arg(source_root);

-- name: UpsertPageCacheWithScheduling :exec
INSERT INTO skipper.skipper_page_cache (
    tenant_id, source_root, page_url, content_hash, etag, last_modified,
    raw_size, last_fetched_at, sitemap_priority, sitemap_changefreq,
    consecutive_unchanged, consecutive_failures, source_type
)
VALUES (
    sqlc.arg(tenant_id), sqlc.arg(source_root), sqlc.arg(page_url),
    sqlc.narg(content_hash), sqlc.narg(etag), sqlc.narg(last_modified),
    sqlc.narg(raw_size), sqlc.arg(last_fetched_at), sqlc.arg(sitemap_priority),
    sqlc.narg(sitemap_changefreq), sqlc.arg(consecutive_unchanged),
    sqlc.arg(consecutive_failures), sqlc.arg(source_type)
)
ON CONFLICT (tenant_id, page_url) DO UPDATE
SET content_hash = EXCLUDED.content_hash,
    etag = EXCLUDED.etag,
    last_modified = EXCLUDED.last_modified,
    raw_size = EXCLUDED.raw_size,
    last_fetched_at = EXCLUDED.last_fetched_at,
    source_root = EXCLUDED.source_root,
    sitemap_priority = EXCLUDED.sitemap_priority,
    sitemap_changefreq = EXCLUDED.sitemap_changefreq,
    source_type = EXCLUDED.source_type;

-- name: ListPageCacheForTenant :many
SELECT tenant_id, source_root, page_url, content_hash, etag, last_modified,
       raw_size, last_fetched_at, sitemap_priority, sitemap_changefreq,
       consecutive_unchanged, consecutive_failures, source_type
FROM skipper.skipper_page_cache
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY last_fetched_at ASC;

-- name: UpdatePageCrawlOutcome :exec
UPDATE skipper.skipper_page_cache
SET consecutive_unchanged = CASE
        WHEN sqlc.arg(changed)::boolean THEN 0
        ELSE consecutive_unchanged + 1
    END,
    consecutive_failures = CASE
        WHEN sqlc.arg(failed)::boolean THEN consecutive_failures + 1
        ELSE 0
    END
WHERE tenant_id = sqlc.arg(tenant_id)
  AND page_url = sqlc.arg(page_url);

-- name: CleanupStalePageCache :execrows
DELETE FROM skipper.skipper_page_cache
WHERE tenant_id = sqlc.arg(tenant_id)
  AND last_fetched_at < sqlc.arg(cutoff);
