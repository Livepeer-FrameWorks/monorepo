-- name: ResolveStripeTaxDocumentOwners :many
-- Authenticated provider events locate only their existing tenant mapping.
SELECT DISTINCT tenant_id::text AS tenant_id FROM (
 (SELECT DISTINCT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id=sqlc.arg(customer_id)::text ORDER BY tenant_id LIMIT 2)
 UNION ALL
 (SELECT DISTINCT tenant_id FROM purser.cluster_subscriptions WHERE stripe_customer_id=sqlc.arg(customer_id)::text ORDER BY tenant_id LIMIT 2)
 UNION ALL
 (SELECT DISTINCT tenant_id FROM purser.payment_provider_intents WHERE provider='stripe' AND provider_customer_id=sqlc.arg(customer_id)::text ORDER BY tenant_id LIMIT 2)
) owned LIMIT 2;

-- name: StoreProviderBillingDocument :one
INSERT INTO purser.provider_billing_documents(tenant_id,provider,provider_account_id,livemode,document_kind,provider_document_id,
 original_number,source_invoice_id,status,currency,net_minor,vat_minor,gross_minor,tax_status,tax_groups,supplier_snapshot,customer_snapshot,raw_facts,tax_assessment,
 original_pdf_url,original_pdf,pdf_sha256,issued_at)
VALUES(sqlc.arg(tenant_id)::text::uuid,'stripe',sqlc.arg(provider_account_id),sqlc.arg(livemode),sqlc.arg(document_kind),sqlc.arg(provider_document_id),
 sqlc.arg(original_number),sqlc.narg(source_invoice_id),sqlc.arg(status),sqlc.arg(currency),sqlc.narg(net_minor),sqlc.narg(vat_minor),sqlc.arg(gross_minor),sqlc.arg(tax_status),
 sqlc.arg(tax_groups)::jsonb,sqlc.arg(supplier_snapshot)::jsonb,sqlc.arg(customer_snapshot)::jsonb,sqlc.arg(raw_facts)::jsonb,sqlc.arg(tax_assessment)::jsonb,
 sqlc.narg(original_pdf_url),sqlc.narg(original_pdf),sqlc.narg(pdf_sha256),sqlc.arg(issued_at))
ON CONFLICT(provider,provider_account_id,livemode,document_kind,provider_document_id) DO UPDATE
SET status=EXCLUDED.status,
 original_pdf_url=COALESCE(purser.provider_billing_documents.original_pdf_url,EXCLUDED.original_pdf_url),
 original_pdf=COALESCE(purser.provider_billing_documents.original_pdf,EXCLUDED.original_pdf),
 pdf_sha256=COALESCE(purser.provider_billing_documents.pdf_sha256,EXCLUDED.pdf_sha256)
WHERE purser.provider_billing_documents.tenant_id=sqlc.arg(tenant_id)::text::uuid
RETURNING id::text AS id,(original_pdf IS NOT NULL)::boolean AS has_pdf;

-- name: GetProviderBillingDocument :one
SELECT id::text AS id,document_kind,provider_document_id,original_number,source_invoice_id,status,currency,net_minor,vat_minor,gross_minor,
 tax_status::text AS tax_status,tax_groups,supplier_snapshot,customer_snapshot,raw_facts,
 tax_assessment::jsonb AS tax_assessment,original_pdf,pdf_sha256,issued_at,retention_until
FROM purser.provider_billing_documents
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(document_id)::text::uuid AND document_kind=sqlc.arg(document_kind);

-- name: StoreProviderTaxProfile :exec
INSERT INTO purser.provider_tax_profiles(tenant_id,profile_sha256,profile)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(profile_sha256),sqlc.arg(profile)::jsonb)
ON CONFLICT(tenant_id,profile_sha256) DO NOTHING;

-- name: GetProviderTaxProfile :one
SELECT profile FROM purser.provider_tax_profiles
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND profile_sha256=sqlc.arg(profile_sha256);

-- name: FindProviderBillingDocument :one
SELECT id::text AS id,provider_account_id,tax_status::text AS tax_status,
 (original_pdf IS NOT NULL)::boolean AS has_pdf, supplier_snapshot,customer_snapshot,
 tax_assessment::jsonb AS tax_assessment
FROM purser.provider_billing_documents
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND provider='stripe' AND provider_document_id=sqlc.arg(provider_document_id) AND document_kind=sqlc.arg(document_kind);

-- name: GetProviderBillingDocumentTaxState :one
SELECT tax_status::text AS tax_status,
 tax_assessment::jsonb AS tax_assessment
FROM purser.provider_billing_documents
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(document_id)::text::uuid AND document_kind=sqlc.arg(document_kind);

-- name: ListTenantStripeTaxSubscriptions :many
SELECT stripe_customer_id::text AS customer_id,COALESCE(stripe_subscription_id,'')::text AS subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stripe_customer_id IS NOT NULL AND stripe_customer_id<>'' AND status NOT IN ('cancelled','ended')
UNION
SELECT stripe_customer_id::text AS customer_id,COALESCE(stripe_subscription_id,'')::text AS subscription_id
FROM purser.cluster_subscriptions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stripe_customer_id IS NOT NULL AND stripe_customer_id<>'' AND status NOT IN ('cancelled','ended');

-- name: ResolveProviderTaxReview :execrows
UPDATE purser.crypto_accounting_anomalies
SET status='resolved',resolved_at=NOW(),resolution_note=sqlc.arg(note)::text,
    evidence_json=evidence_json || '{"provider_tax_sync_complete":true}'::jsonb
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND network='stripe' AND kind='provider_tax_review'
  AND reference_type=sqlc.arg(reference_type)::text AND reference_id=sqlc.arg(reference_id)::text
  AND (status='open' OR NOT evidence_json @> '{"provider_tax_sync_complete":true}'::jsonb);

-- name: ResolveProviderDocumentTaxReview :execrows
UPDATE purser.crypto_accounting_anomalies
SET status='resolved',resolved_at=NOW(),resolution_note='Retained original provider tax facts verified'
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND network='stripe' AND kind='provider_tax_document_review'
 AND reference_type=sqlc.arg(document_kind)::text AND reference_id=sqlc.arg(document_id)::text AND status='open';
