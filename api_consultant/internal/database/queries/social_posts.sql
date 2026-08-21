-- name: SaveSocialPost :one
INSERT INTO skipper.skipper_posts (
    tenant_id, content_type, tweet_text, context_summary,
    trigger_data, status, created_at
)
VALUES (
    sqlc.arg(tenant_id), sqlc.arg(content_type), sqlc.arg(tweet_text),
    sqlc.narg(context_summary), sqlc.arg(trigger_data)::text::jsonb,
    sqlc.arg(status), NOW()
)
RETURNING id, created_at;

-- name: CountSocialPostsToday :one
SELECT COUNT(*)
FROM skipper.skipper_posts
WHERE tenant_id = sqlc.arg(tenant_id)
  AND status IN ('draft', 'sent', 'posted')
  AND created_at >= (CURRENT_DATE AT TIME ZONE 'UTC');

-- name: ListRecentSocialPosts :many
SELECT id, content_type, tweet_text, context_summary,
       COALESCE(trigger_data, 'null') AS trigger_data,
       status, sent_at, created_at
FROM skipper.skipper_posts
WHERE tenant_id = sqlc.arg(tenant_id)
  AND status IN ('draft', 'sent', 'posted', 'baseline')
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: MarkSocialPostSent :exec
UPDATE skipper.skipper_posts
SET status = 'sent', sent_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id);
