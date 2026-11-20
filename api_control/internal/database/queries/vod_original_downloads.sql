-- name: ConsumeVodOriginalDownloadGrant :one
INSERT INTO commodore.vod_original_download_limits (tenant_id, next_grant_at)
VALUES (sqlc.arg(tenant_id)::uuid,
        statement_timestamp() + sqlc.arg(interval_seconds)::int * INTERVAL '1 second')
ON CONFLICT (tenant_id) DO UPDATE
SET next_grant_at = GREATEST(commodore.vod_original_download_limits.next_grant_at, statement_timestamp())
                    + sqlc.arg(interval_seconds)::int * INTERVAL '1 second'
WHERE commodore.vod_original_download_limits.next_grant_at
      <= statement_timestamp() + sqlc.arg(burst_seconds)::int * INTERVAL '1 second'
RETURNING tenant_id::text;
