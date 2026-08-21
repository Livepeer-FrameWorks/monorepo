-- name: SaveReport :one
INSERT INTO skipper.skipper_reports (
    id, tenant_id, trigger, summary, metrics_reviewed,
    root_cause, recommendations, created_at
)
VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(trigger)::text,
    sqlc.arg(summary)::text, sqlc.arg(metrics_reviewed)::text::jsonb,
    sqlc.arg(root_cause)::text, sqlc.arg(recommendations)::text::jsonb, NOW()
)
RETURNING created_at;

-- name: ListReportsByTenant :many
SELECT id, tenant_id, trigger, summary,
       COALESCE(metrics_reviewed, 'null') AS metrics_reviewed,
       root_cause, COALESCE(recommendations, 'null') AS recommendations,
       created_at, read_at
FROM skipper.skipper_reports
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: CountReportsByTenant :one
SELECT COUNT(*)
FROM skipper.skipper_reports
WHERE tenant_id = sqlc.arg(tenant_id);

-- name: ListReportsByTenantPaginated :many
SELECT id, tenant_id, trigger, summary,
       COALESCE(metrics_reviewed, 'null') AS metrics_reviewed,
       root_cause, COALESCE(recommendations, 'null') AS recommendations,
       created_at, read_at
FROM skipper.skipper_reports
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: GetReportByID :one
SELECT id, tenant_id, trigger, summary,
       COALESCE(metrics_reviewed, 'null') AS metrics_reviewed,
       root_cause, COALESCE(recommendations, 'null') AS recommendations,
       created_at, read_at
FROM skipper.skipper_reports
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: MarkAllReportsRead :execrows
UPDATE skipper.skipper_reports
SET read_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)
  AND read_at IS NULL;

-- name: MarkReportsRead :execrows
UPDATE skipper.skipper_reports
SET read_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)
  AND id = ANY(sqlc.arg(ids)::text[])
  AND read_at IS NULL;

-- name: CountUnreadReports :one
SELECT COUNT(*)
FROM skipper.skipper_reports
WHERE tenant_id = sqlc.arg(tenant_id)
  AND read_at IS NULL;
