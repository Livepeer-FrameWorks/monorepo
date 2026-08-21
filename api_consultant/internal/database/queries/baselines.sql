-- name: GetBaselines :many
SELECT metric_name, avg_value, m2, sample_count
FROM skipper.skipper_baselines
WHERE tenant_id = sqlc.arg(tenant_id)
  AND stream_id = sqlc.arg(stream_id);

-- name: UpsertBaseline :exec
INSERT INTO skipper.skipper_baselines (
    tenant_id, stream_id, metric_name, avg_value, m2, sample_count, updated_at
)
VALUES (
    sqlc.arg(tenant_id), sqlc.arg(stream_id), sqlc.arg(metric_name),
    sqlc.arg(avg_value), sqlc.arg(m2), sqlc.arg(sample_count), NOW()
)
ON CONFLICT (tenant_id, stream_id, metric_name) DO UPDATE
SET avg_value = EXCLUDED.avg_value,
    m2 = EXCLUDED.m2,
    sample_count = EXCLUDED.sample_count,
    updated_at = NOW();

-- name: CleanupStaleBaselines :execrows
DELETE FROM skipper.skipper_baselines
WHERE tenant_id = sqlc.arg(tenant_id)
  AND updated_at < sqlc.arg(cutoff);
