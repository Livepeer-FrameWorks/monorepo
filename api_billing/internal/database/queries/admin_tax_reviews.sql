-- name: ListBillingTaxReviews :many
SELECT to_jsonb(review)::jsonb AS payload
FROM purser.crypto_accounting_anomalies review
WHERE review.tenant_id=sqlc.arg(tenant_id)::text::uuid
 AND review.kind=ANY(sqlc.arg(kinds)::text[])
 AND (sqlc.arg(provider)::text='' OR review.network=sqlc.arg(provider)::text)
 AND (sqlc.arg(category)::text='' OR review.kind=sqlc.arg(category)::text)
 AND (sqlc.arg(review_status)::text='all' OR review.status=sqlc.arg(review_status)::text)
 AND review.id>COALESCE(NULLIF(sqlc.arg(after_id)::text,''),'00000000-0000-0000-0000-000000000000')::uuid
ORDER BY review.id LIMIT sqlc.arg(row_limit)::bigint;

-- name: LockBillingTaxReview :one
SELECT to_jsonb(review)::jsonb AS payload
FROM purser.crypto_accounting_anomalies review
WHERE review.tenant_id=sqlc.arg(tenant_id)::text::uuid AND review.id=sqlc.arg(review_id)::text::uuid
 AND review.kind<>'operator_tax_review_acknowledgement'
FOR UPDATE;

-- name: AcknowledgeBillingTaxReview :execrows
UPDATE purser.crypto_accounting_anomalies
SET status='resolved',resolved_at=NOW(),resolved_by=sqlc.arg(actor_id)::text::uuid,
 resolution_note=sqlc.arg(reason)::text
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(review_id)::text::uuid AND status='open';

-- name: RecordBillingTaxReviewAcknowledgement :exec
INSERT INTO purser.crypto_accounting_anomalies(
 tenant_id,kind,network,reference_type,reference_id,detail,evidence_json,status,resolved_at,resolved_by,resolution_note
) VALUES (
 sqlc.arg(tenant_id)::text::uuid,'operator_tax_review_acknowledgement',sqlc.arg(provider)::text,
 'tax_review_acknowledgement',sqlc.arg(audit_id)::text,sqlc.arg(reason)::text,
 sqlc.arg(evidence)::jsonb,'resolved',NOW(),sqlc.arg(actor_id)::text::uuid,sqlc.arg(reason)::text
);
