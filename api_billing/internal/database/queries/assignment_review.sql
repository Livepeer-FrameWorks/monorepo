-- name: GetAssignmentReviewSource :one
SELECT jsonb_build_object(
    'subscription', (SELECT to_jsonb(s) FROM purser.tenant_subscriptions s WHERE s.tenant_id = sqlc.arg(tenant_id)::text::uuid),
    'wallet', (SELECT jsonb_build_object('balance', b.balance_cents, 'currency', b.currency) FROM purser.prepaid_balances b WHERE b.tenant_id = sqlc.arg(tenant_id)::text::uuid AND b.currency = 'EUR'),
    'tiers', (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM purser.billing_tiers t WHERE t.tier_name = sqlc.arg(target_tier) OR t.id = (SELECT s.tier_id FROM purser.tenant_subscriptions s WHERE s.tenant_id = sqlc.arg(tenant_id)::text::uuid)),
    'prices', (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.meter) FROM purser.subscription_pricing_overrides p WHERE p.subscription_id = (SELECT s.id FROM purser.tenant_subscriptions s WHERE s.tenant_id = sqlc.arg(tenant_id)::text::uuid)),
    'invoices', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM purser.billing_invoices i WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid AND (i.status IN ('draft','pending','overdue','failed','manual_review') OR i.base_fee_period_end > NOW() - INTERVAL '1 day')),
    'catalog_prices', (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.tier_id,p.meter) FROM purser.tier_pricing_rules p WHERE p.tier_id IN (SELECT t.id FROM purser.billing_tiers t WHERE t.tier_name=sqlc.arg(target_tier) OR t.id=(SELECT s.tier_id FROM purser.tenant_subscriptions s WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid))),
    'catalog_entitlements', (SELECT jsonb_agg(to_jsonb(e) ORDER BY e.tier_id,e.key) FROM purser.tier_entitlements e WHERE e.tier_id IN (SELECT t.id FROM purser.billing_tiers t WHERE t.tier_name=sqlc.arg(target_tier) OR t.id=(SELECT s.tier_id FROM purser.tenant_subscriptions s WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid))),
    'entitlement_overrides', (SELECT jsonb_agg(to_jsonb(e) ORDER BY e.key) FROM purser.subscription_entitlement_overrides e JOIN purser.tenant_subscriptions s ON s.id=e.subscription_id WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid),
    'collection', (SELECT jsonb_agg(to_jsonb(c) ORDER BY c.currency) FROM purser.billing_collection_balances c WHERE c.tenant_id = sqlc.arg(tenant_id)::text::uuid)
)::jsonb AS source;

-- name: GetAssignmentReviewWallet :one
SELECT COALESCE((SELECT b.balance_cents FROM purser.prepaid_balances b WHERE b.tenant_id = sqlc.arg(tenant_id)::text::uuid AND b.currency = 'EUR'), 0)::bigint AS balance_cents;

-- name: BeginBillingPreview :exec
SELECT set_config('frameworks.billing_preview','true',true);
