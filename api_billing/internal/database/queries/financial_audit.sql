-- name: FinancialAuditPage :many
WITH records AS (
    (SELECT 'invoices'::text AS source, i.id
     FROM purser.billing_invoices i
     WHERE i.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='invoices'
       AND (COALESCE(i.finalized_at,i.created_at) IS NULL OR (COALESCE(i.finalized_at,i.created_at)>=sqlc.arg(from_at)::timestamptz AND COALESCE(i.finalized_at,i.created_at)<sqlc.arg(through_at)::timestamptz))
       AND i.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND i.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY i.id LIMIT 101)
    UNION ALL
    (SELECT 'payments'::text AS source, p.id
     FROM purser.billing_payments p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='payments'
       AND (p.created_at IS NULL OR (p.created_at>=sqlc.arg(from_at)::timestamptz AND p.created_at<sqlc.arg(through_at)::timestamptz))
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
    UNION ALL
    (SELECT 'simplified_invoices'::text AS source, i.id
     FROM purser.simplified_invoices i
     WHERE i.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='simplified_invoices'
       AND (i.issued_at IS NULL OR (i.issued_at>=sqlc.arg(from_at)::timestamptz AND i.issued_at<sqlc.arg(through_at)::timestamptz))
       AND i.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND i.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY i.id LIMIT 101)
    UNION ALL
    (SELECT 'crypto_invoices'::text AS source, i.id
     FROM purser.crypto_invoices i
     WHERE i.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='crypto_invoices'
       AND (i.issued_at IS NULL OR (i.issued_at>=sqlc.arg(from_at)::timestamptz AND i.issued_at<sqlc.arg(through_at)::timestamptz))
       AND i.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND i.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY i.id LIMIT 101)
    UNION ALL
    (SELECT 'credit_notes'::text AS source, n.id
     FROM purser.credit_notes n
     WHERE n.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='credit_notes'
       AND (n.issued_at IS NULL OR (n.issued_at>=sqlc.arg(from_at)::timestamptz AND n.issued_at<sqlc.arg(through_at)::timestamptz))
       AND n.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND n.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY n.id LIMIT 101)
    UNION ALL
    (SELECT 'provider_settlements'::text AS source, p.id
     FROM purser.provider_settlements p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='provider_settlements'
       AND (p.created_at IS NULL OR (p.created_at>=sqlc.arg(from_at)::timestamptz AND p.created_at<sqlc.arg(through_at)::timestamptz))
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
    UNION ALL
    (SELECT 'pending_topups'::text AS source, p.id
     FROM purser.pending_topups p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='pending_topups'
       AND (p.created_at IS NULL OR (p.created_at>=sqlc.arg(from_at)::timestamptz AND p.created_at<sqlc.arg(through_at)::timestamptz))
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
    UNION ALL
    (SELECT 'balance_transactions'::text AS source, p.id
     FROM purser.balance_transactions p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='balance_transactions'
       AND (p.created_at IS NULL OR (p.created_at>=sqlc.arg(from_at)::timestamptz AND p.created_at<sqlc.arg(through_at)::timestamptz))
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
    UNION ALL
    (SELECT 'business_establishment_reviews'::text AS source,r.id
     FROM purser.business_establishment_reviews r
     WHERE r.tenant_id=sqlc.arg(tenant_id)::text::uuid
       AND sqlc.arg(source)::text='business_establishment_reviews'
       AND r.created_at>=sqlc.arg(from_at)::timestamptz AND r.created_at<sqlc.arg(through_at)::timestamptz
       AND r.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND r.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY r.id LIMIT 101)
    UNION ALL
    (SELECT 'provider_billing_documents'::text AS source,p.id FROM purser.provider_billing_documents p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid AND sqlc.arg(source)::text='provider_billing_documents'
       AND p.captured_at>=sqlc.arg(from_at)::timestamptz AND p.captured_at<sqlc.arg(through_at)::timestamptz
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
    UNION ALL
    (SELECT 'provider_tax_profiles'::text AS source,p.id FROM purser.provider_tax_profiles p
     WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid AND sqlc.arg(source)::text='provider_tax_profiles'
       AND p.created_at>=sqlc.arg(from_at)::timestamptz AND p.created_at<sqlc.arg(through_at)::timestamptz
       AND p.id>=COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
       AND p.id IS DISTINCT FROM NULLIF(sqlc.arg(after_id)::text,'')::uuid
     ORDER BY p.id LIMIT 101)
), page AS MATERIALIZED (
    SELECT source,id FROM records ORDER BY id LIMIT 101
)
SELECT page.id::text AS id,
       COALESCE(CASE page.source
           WHEN 'invoices' THEN (
               SELECT (to_jsonb(i)||jsonb_build_object('vies_revalidation',
                 (SELECT a.evidence_json->>'revalidation_outcome' FROM purser.crypto_accounting_anomalies a
                  WHERE a.tenant_id=sqlc.arg(tenant_id)::text::uuid AND a.kind='vies_revalidation'
                    AND a.reference_type='vat_validation' AND a.reference_id=sqlc.arg(tenant_id)::text||':'||(i.tax_decision->'vat_validation'->>'vat_number_hash')
                  ORDER BY a.last_seen_at DESC,a.id DESC LIMIT 1)))::text
               FROM purser.billing_invoices i
               WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid AND i.id = page.id
           )
           WHEN 'payments' THEN (
               SELECT to_jsonb(p)::text
               FROM purser.billing_payments p
               WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid AND p.id = page.id
           )
           WHEN 'simplified_invoices' THEN (
               SELECT to_jsonb(i)::text FROM purser.simplified_invoices i
               WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid AND i.id = page.id
           )
           WHEN 'crypto_invoices' THEN (
               SELECT to_jsonb(i)::text FROM purser.crypto_invoices i
               WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid AND i.id = page.id
           )
           WHEN 'credit_notes' THEN (
               SELECT to_jsonb(n)::text FROM purser.credit_notes n
               WHERE n.tenant_id = sqlc.arg(tenant_id)::text::uuid AND n.id = page.id
           )
           WHEN 'provider_settlements' THEN (
               SELECT to_jsonb(p)::text FROM purser.provider_settlements p
               WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid AND p.id = page.id
           )
           WHEN 'pending_topups' THEN (
               SELECT to_jsonb(p)::text FROM purser.pending_topups p
               WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid AND p.id = page.id
           )
           WHEN 'balance_transactions' THEN (
               SELECT to_jsonb(p)::text FROM purser.balance_transactions p
               WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid AND p.id = page.id
           )
           WHEN 'business_establishment_reviews' THEN (
               SELECT to_jsonb(r)::text FROM purser.business_establishment_reviews r
               WHERE r.tenant_id=sqlc.arg(tenant_id)::text::uuid AND r.id=page.id
           )
           WHEN 'provider_billing_documents' THEN (
             SELECT ((to_jsonb(p)-'original_pdf') || jsonb_build_object(
               'captured_tax_status',p.tax_status,'captured_tax_assessment',p.tax_assessment,
               'tax_status',p.tax_status,
               'tax_assessment',p.tax_assessment,'vies_revalidation',
               (SELECT a.evidence_json->>'revalidation_outcome' FROM purser.crypto_accounting_anomalies a
                WHERE a.tenant_id=sqlc.arg(tenant_id)::text::uuid AND a.kind='vies_revalidation'
                  AND a.reference_id=sqlc.arg(tenant_id)::text || ':' || (p.tax_assessment->'evidence'->'vat_validation'->>'vat_number_hash')
                ORDER BY a.last_seen_at DESC,a.id DESC LIMIT 1)))::text
             FROM purser.provider_billing_documents p WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid AND p.id=page.id)
           WHEN 'provider_tax_profiles' THEN (SELECT (to_jsonb(p))::text FROM purser.provider_tax_profiles p WHERE p.tenant_id=sqlc.arg(tenant_id)::text::uuid AND p.id=page.id)
       END, '{}')::text AS payload
FROM page
ORDER BY page.id;

-- name: FinancialAuditPaymentOwnershipReady :one
SELECT NOT EXISTS(
 SELECT 1 FROM purser.billing_payments payment
 JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id
 WHERE invoice.tenant_id=sqlc.arg(tenant_id)::text::uuid AND payment.tenant_id IS NULL
)::boolean AS ready;
