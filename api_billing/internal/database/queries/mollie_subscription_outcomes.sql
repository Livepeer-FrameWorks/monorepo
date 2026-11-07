-- name: ClaimNativeMollieSubscriptionOutcome :execrows
INSERT INTO purser.webhook_events(provider,event_id,event_type,status,processed_at)
SELECT 'mollie',sqlc.arg(outcome_id),'native_subscription_outcome','processed',NOW()
FROM purser.tenant_subscriptions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
ON CONFLICT(provider,event_id) DO NOTHING;

-- name: GetNativeMollieInvoiceID :one
SELECT id::text FROM purser.billing_invoices
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND source_reference_type='mollie_native_payment' AND source_reference_id=sqlc.arg(payment_id);

-- name: NativeMollieInvoiceHasAmountReview :one
SELECT EXISTS (
 SELECT 1 FROM purser.billing_invoices
 WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(invoice_id)::text::uuid
  AND tax_decision->>'code'='accepted_tax_gross_mismatch'
  AND document_gross_minor<(tax_decision->>'expected_gross_minor')::bigint
)::boolean AS amount_review;
