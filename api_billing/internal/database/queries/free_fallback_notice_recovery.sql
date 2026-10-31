-- name: GetFreeFallbackNoticeRecovery :one
SELECT COALESCE(billing_email, '')::text AS recipient,
       free_fallback_at::timestamptz AS scheduled_at,
       (free_fallback_moved_at IS NOT NULL)::boolean AS moved,
       free_fallback_notice_abandoned_at,
       COALESCE(free_fallback_notice_error, '')::text AS last_error
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at IS NOT NULL
  AND (free_fallback_moved_at IS NOT NULL OR free_fallback_notified_at IS NULL);

-- name: RequeueAbandonedFreeFallbackNotice :execrows
UPDATE purser.tenant_subscriptions
SET free_fallback_notice_abandoned_at = NULL,
    free_fallback_notice_attempted_at = NULL,
    free_fallback_notice_failures = 0,
    free_fallback_notice_error = NULL,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND (free_fallback_moved_at IS NOT NULL) = sqlc.arg(moved)::boolean
  AND free_fallback_notice_abandoned_at IS NOT NULL;
