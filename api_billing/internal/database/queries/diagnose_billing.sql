-- name: GetBillingDiagnosis :one
SELECT jsonb_build_object(
  'tenant_id', s.tenant_id, 'status', s.status, 'tier', t.tier_name,
  'billing_model', s.billing_model,
  'period_start', COALESCE(s.billing_period_start::text, ''),
  'period_end', COALESCE(s.billing_period_end::text, ''),
  'provider', CASE WHEN s.stripe_subscription_id IS NOT NULL THEN 'stripe'
                   WHEN s.mollie_subscription_id IS NOT NULL THEN 'mollie'
                   ELSE COALESCE(s.payment_method, '') END,
  'stripe_customer_id', COALESCE(s.stripe_customer_id, ''),
  'stripe_subscription_id', COALESCE(s.stripe_subscription_id, ''),
  'stripe_price_id', COALESCE(t.stripe_price_id_monthly, ''),
  'mollie_subscription_id', COALESCE(s.mollie_subscription_id, ''),
  'pending_tier_id', COALESCE(s.pending_tier_id::text, ''),
  'pending_effective_at', COALESCE(s.pending_effective_at::text, ''),
  'pending_provider_tier_id', COALESCE(s.pending_provider_tier_id::text, ''),
  'free_fallback_at', COALESCE(s.free_fallback_at::text, ''),
  'free_fallback_reason', COALESCE(s.free_fallback_reason, ''),
  'intent_count', (SELECT count(*) FROM purser.payment_provider_intents p WHERE p.tenant_id = s.tenant_id),
  'invoice_count', (SELECT count(*) FROM purser.billing_invoices i WHERE i.tenant_id = s.tenant_id),
  'intents', COALESCE((SELECT jsonb_agg(page.item ORDER BY page.created_at DESC, page.id DESC) FROM (
    SELECT p.id, p.created_at, jsonb_build_object(
      'id', p.id, 'provider', p.provider, 'purpose', p.purpose, 'status', p.status,
      'submission_state', p.submission_state, 'amount_cents', p.amount_cents,
      'currency', p.currency, 'attempts', p.attempt_count,
      'has_error', p.last_error IS NOT NULL, 'created_at', p.created_at::text) AS item
    FROM purser.payment_provider_intents p WHERE p.tenant_id = s.tenant_id
    ORDER BY p.created_at DESC, p.id DESC LIMIT 20
  ) page), '[]'::jsonb),
  'invoices', COALESCE((SELECT jsonb_agg(page.item ORDER BY page.created_at DESC, page.id DESC) FROM (
    SELECT i.id, i.created_at, jsonb_build_object(
      'id', i.id, 'number', i.invoice_number, 'status', i.status,
      'amount', i.amount::text, 'currency', i.currency, 'due_at', i.due_date::text) AS item
    FROM purser.billing_invoices i WHERE i.tenant_id = s.tenant_id
    ORDER BY i.created_at DESC, i.id DESC LIMIT 20
  ) page), '[]'::jsonb),
  'balances', COALESCE((SELECT jsonb_agg(jsonb_build_object(
      'tenant_id', b.tenant_id, 'balance_cents', b.balance_cents, 'currency', b.currency)
      ORDER BY b.currency) FROM purser.prepaid_balances b WHERE b.tenant_id = s.tenant_id), '[]'::jsonb)
)::jsonb AS diagnosis
FROM purser.tenant_subscriptions s
JOIN purser.billing_tiers t ON t.id = s.tier_id
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid;
