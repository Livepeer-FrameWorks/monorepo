-- name: GetRetainedProviderAssignment :one
-- Local invoice coordinates can advance while native cancellation evidence is
-- retained; the cancellation identity and deadline fence each prepared close.
SELECT stripe_subscription_id, mollie_subscription_id,
       free_fallback_provider_ended_at, free_fallback_at,
       stripe_current_period_end,
       NOW()::timestamptz AS database_now
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND billing_model = 'postpaid'
  AND free_fallback_provider_ended_at IS NOT NULL
  AND (NULLIF(stripe_subscription_id, '') IS NOT NULL OR NULLIF(mollie_subscription_id, '') IS NOT NULL);
