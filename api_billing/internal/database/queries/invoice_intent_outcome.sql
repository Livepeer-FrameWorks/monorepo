-- name: ListUnreconciledInvoiceIntentTenants :many
SELECT DISTINCT intent.tenant_id::text AS tenant_id
FROM purser.payment_provider_intents intent
WHERE intent.tenant_id > sqlc.arg(after_tenant_id)::text::uuid
  AND intent.purpose IN ('stripe_overage_charge', 'mollie_overage_charge')
  AND intent.status <> 'succeeded'
  AND EXISTS (SELECT 1 FROM purser.billing_payments payment WHERE payment.intent_id=intent.id AND payment.status='confirmed')
ORDER BY tenant_id LIMIT 100;

-- name: ReconcileConfirmedInvoiceIntents :execrows
-- A confirmed receipt is evidence of capture even when the API response was lost.
-- Match the recorded owner, invoice, provider, currency and original minor amount.
UPDATE purser.payment_provider_intents intent
SET status='succeeded', succeeded_at=COALESCE(intent.succeeded_at,payment.confirmed_at,NOW()),
    provider_payment_id=COALESCE(NULLIF(intent.provider_payment_id,''),payment.tx_id),
    last_error=NULL, updated_at=NOW()
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id
WHERE intent.tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND invoice.tenant_id=intent.tenant_id
  AND payment.intent_id=intent.id
  AND intent.local_reference_type='invoice' AND intent.local_reference_id=invoice.id
  AND intent.purpose=intent.provider || '_overage_charge'
  AND payment.method='card' AND payment.status='confirmed'
  AND NULLIF(payment.tx_id,'') IS NOT NULL
  AND (NULLIF(intent.provider_payment_id,'') IS NULL OR intent.provider_payment_id=payment.tx_id)
  AND intent.currency=payment.original_currency AND intent.amount_cents=payment.original_amount_cents
  AND intent.status <> 'succeeded';
