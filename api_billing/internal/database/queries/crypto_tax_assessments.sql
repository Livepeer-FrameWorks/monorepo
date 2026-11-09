-- name: GetVATValidationAtQuote :one
SELECT evidence.valid, to_jsonb(evidence)::jsonb AS validation_snapshot,
       (evidence.expires_at > sqlc.arg(quoted_at)::timestamptz)::boolean AS fresh_at_quote
FROM purser.vat_validation_evidence evidence
WHERE evidence.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND evidence.vat_number_hash = sqlc.arg(vat_number_hash)
  AND evidence.checked_at <= sqlc.arg(quoted_at)::timestamptz
ORDER BY evidence.checked_at DESC, evidence.id DESC
LIMIT 1;
