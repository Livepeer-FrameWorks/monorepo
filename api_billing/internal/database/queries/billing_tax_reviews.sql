-- name: RecordBillingTaxReview :exec
INSERT INTO purser.crypto_accounting_anomalies (
 tenant_id,kind,network,reference_type,reference_id,amount_cents,currency,detail,evidence_json
) VALUES (
 sqlc.arg(tenant_id)::text::uuid,sqlc.arg(kind),sqlc.arg(network),sqlc.arg(reference_type),sqlc.arg(reference_id),
 sqlc.arg(amount_cents),'EUR',sqlc.arg(detail),sqlc.arg(evidence_json)::jsonb
)
ON CONFLICT (kind,reference_type,reference_id) DO UPDATE
SET last_seen_at=NOW(),occurrences=purser.crypto_accounting_anomalies.occurrences+1,
 detail=EXCLUDED.detail,evidence_json=EXCLUDED.evidence_json,status='open',
 resolved_at=NULL,resolved_by=NULL,resolution_note=NULL
WHERE purser.crypto_accounting_anomalies.tenant_id=sqlc.arg(tenant_id)::text::uuid
 AND purser.crypto_accounting_anomalies.evidence_json IS DISTINCT FROM EXCLUDED.evidence_json;

-- name: HasBillingTaxReviewReceipt :one
SELECT EXISTS (
 SELECT 1 FROM purser.crypto_accounting_anomalies
 WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND kind=sqlc.arg(kind)
  AND reference_type=sqlc.arg(reference_type) AND reference_id=sqlc.arg(reference_id)
  AND evidence_json=sqlc.arg(evidence_json)::jsonb
)::boolean;
