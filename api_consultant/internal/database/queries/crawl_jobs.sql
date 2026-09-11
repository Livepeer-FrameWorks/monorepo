-- name: CreateCrawlJob :execrows
INSERT INTO skipper.skipper_crawl_jobs (id, tenant_id, sitemap_url, status, started_at)
SELECT sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(sitemap_url), 'running', sqlc.arg(started_at)
WHERE NOT EXISTS (
    SELECT 1
    FROM skipper.skipper_crawl_jobs
    WHERE tenant_id = sqlc.arg(tenant_id)
      AND sitemap_url = sqlc.arg(sitemap_url)
      AND status = 'running'
);

-- name: FinishRunningCrawlJob :exec
UPDATE skipper.skipper_crawl_jobs
SET status = sqlc.arg(status), error = sqlc.narg(error), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id)
  AND status = 'running';

-- name: GetCrawlJob :one
SELECT id, tenant_id, sitemap_url, status, error, started_at, finished_at
FROM skipper.skipper_crawl_jobs
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: ListCrawlJobs :many
SELECT id, tenant_id, sitemap_url, status, error, started_at, finished_at
FROM skipper.skipper_crawl_jobs
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY started_at DESC
LIMIT 50;

-- name: GetCrawlJobStatus :one
SELECT status
FROM skipper.skipper_crawl_jobs
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: CancelRunningCrawlJob :exec
UPDATE skipper.skipper_crawl_jobs
SET status = 'cancelled', finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id)
  AND status = 'running';

-- name: CleanupFinishedCrawlJobs :execrows
DELETE FROM skipper.skipper_crawl_jobs
WHERE tenant_id = sqlc.arg(tenant_id)
  AND finished_at < sqlc.arg(cutoff);

-- name: GetKnowledgeSample :one
SELECT chunk_text
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_url = sqlc.arg(source_url)
ORDER BY chunk_index ASC
LIMIT 1;

-- name: ReapAbandonedCrawlJobs :execrows
-- A crawl settles only in the tail of the in-memory goroutine that runs it, so a
-- restart, deploy or OOM inside the crawl window leaves its row 'running'
-- forever. CleanupFinishedCrawlJobs cannot reap it (finished_at IS NULL, and
-- NULL < cutoff is NULL), and CreateCrawlJob refuses while one is running, so
-- every later crawl of that sitemap is rejected with a conflict indefinitely.
--
-- The cutoff is past the crawl context's own deadline: a job still owned by a
-- live process settles itself before that, so anything older has no owner.
UPDATE skipper.skipper_crawl_jobs
SET status = 'failed',
    error = 'crawl abandoned: no owning process settled it before its deadline',
    finished_at = sqlc.arg(finished_at)
WHERE status = 'running'
  AND started_at < sqlc.arg(cutoff);
